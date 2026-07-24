#!/usr/bin/env bash
# Самодокументируемое демо Дня 2. Печатает весь пайплайн по шагам — для видео без озвучки.
# Запускать ИЗ КОРНЯ репозитория: bash demo-day2.sh
set -uo pipefail

TASK="week-1/task-2/notes-api"
PORT=8080
OUT="$HOME/day2-evidence"
mkdir -p "$OUT"

step()  { printf '\n\033[1;36m════ %s ════\033[0m\n\n' "$1"; }
say()   { printf '\033[0;36m%s\033[0m\n' "$1"; }
ok()    { printf '\033[1;32m  ✓ %s\033[0m\n' "$1"; }
bad()   { printf '\033[1;31m  ✗ %s\033[0m\n' "$1"; }
pause() { printf '\n\033[0;90m--- дальше: %s ---\033[0m\n' "$1"; sleep 2; }

reset_tree() { git checkout -- . >/dev/null 2>&1; git clean -fdq; }

srv_start() {
  pkill -f "notes-api" >/dev/null 2>&1 || true
  sleep 1
  go run ./"$TASK"/cmd/notes-api > /tmp/day2-srv.log 2>&1 &
  for _ in $(seq 1 25); do
    curl -s -o /dev/null "http://localhost:$PORT/notes" && return 0
    sleep 0.4
  done
  bad "сервис не поднялся, см. /tmp/day2-srv.log"; return 1
}
srv_stop() { pkill -f "notes-api" >/dev/null 2>&1 || true; sleep 1; }

probe() {  # печатает HTTP-код запроса к несуществующей заметке
  curl -s -o /tmp/day2-body.txt -w "%{http_code}" "http://localhost:$PORT/notes/does-not-exist"
}

# ─────────────────────────────────────────────────────────────
step "ШАГ 0. Что настроено"

say "Селектор — раздел в проектном CLAUDE.md, выбирает профиль по типу запроса:"
grep -A7 '^| Признак' "$TASK/CLAUDE.md" || true

say ""
say "Профили — режимы работы главного агента:"
for f in "$TASK"/.claude/profiles/*.md; do printf '   %s — %s\n' "$(basename "$f")" "$(sed -n '3p' "$f")"; done

say ""
say "Субагенты — исполнители с УРЕЗАННЫМ набором инструментов:"
for f in "$TASK"/.claude/agents/*.md; do
  printf '   %-12s tools:%s\n' "$(basename "$f")" "$(grep '^tools:' "$f" | cut -d: -f2-)"
done
say ""
say "У research нет Edit/Write/Bash — запрет «не менять код» структурный, а не текстовый."

say ""
say "Скиллы — процедуры, подгружаемые по необходимости:"
for f in "$TASK"/.claude/skills/*/SKILL.md; do
  printf '   %-18s %s\n' "$(basename "$(dirname "$f")")" "$(sed -n '4p' "$f" | sed 's/^ *//')"
done

pause "воспроизводим баг ДО починки"

# ─────────────────────────────────────────────────────────────
step "ШАГ 1. Симптом до починки"

reset_tree
srv_start || exit 1
CODE_BEFORE=$(probe)
say "GET /notes/does-not-exist  →  HTTP $CODE_BEFORE"
say "тело ответа: $(cat /tmp/day2-body.txt)"
if [ "$CODE_BEFORE" = "200" ]; then
  bad "БАГ ВОСПРОИЗВЁЛСЯ: несуществующая заметка отдаёт 200 и пустой объект вместо 404"
else
  ok "неожиданно: код $CODE_BEFORE (баг не воспроизвёлся)"
fi
srv_stop

say ""
say "При этом статические проверки ничего не замечают:"
go build ./"$TASK"/... 2>&1 && ok "go build — чисто"
go vet   ./"$TASK"/... 2>&1 && ok "go vet — чисто"
go test  ./"$TASK"/... 2>&1 | tail -4
say "Тесты зелёные: баг не покрыт ни одним тестом."

pause "запускаем профиль bug-fix"

# ─────────────────────────────────────────────────────────────
step "ШАГ 2. Профиль Bug Fix — один запуск, без подсказок"

PROMPT_BUG='GET /notes/<любой несуществующий id> возвращает 200 и пустую заметку вместо 404. Ожидаю 404 и тело с ошибкой. Разберись и почини.'
say "Промпт: $PROMPT_BUG"
say ""

( cd "$TASK" && claude -p "$PROMPT_BUG" \
    --permission-mode acceptEdits \
    --allowedTools "Read,Glob,Grep,Bash,Edit,Write,Task" \
    < /dev/null ) 2>&1 | tee "$OUT/bugfix-run.txt"

pause "проверяем, что сделал агент"

# ─────────────────────────────────────────────────────────────
step "ШАГ 3. Что изменилось и работает ли фикс"

say "Диф, который внёс агент:"
git --no-pager diff --stat
git --no-pager diff > "$OUT/bugfix.patch"
say ""
git --no-pager diff -- "$TASK/internal/storage/memory.go" | head -40

say ""
say "Симптом после починки:"
srv_start || exit 1
CODE_AFTER=$(probe)
say "GET /notes/does-not-exist  →  HTTP $CODE_AFTER"
say "тело ответа: $(cat /tmp/day2-body.txt)"
[ "$CODE_AFTER" = "404" ] && ok "СИМПТОМ УСТРАНЁН: теперь 404" || bad "симптом остался: $CODE_AFTER"

say ""
say "Контроль: обычный сценарий не сломан —"
NID=$(curl -s -X POST -d '{"text":"проверочная заметка"}' "http://localhost:$PORT/notes" \
      | python3 -c 'import sys,json;print(json.load(sys.stdin)["id"])' 2>/dev/null)
curl -s -w "  <- HTTP %{http_code}\n" "http://localhost:$PORT/notes/$NID"
srv_stop

say ""
say "Проверки после фикса:"
{ go build ./"$TASK"/... 2>&1 && echo "build OK"
  go vet   ./"$TASK"/... 2>&1 && echo "vet OK"
  echo "gofmt:"; gofmt -l "$TASK"
  go test  ./"$TASK"/... -count=1 2>&1
} | tee "$OUT/bugfix-checks.txt"

pause "откат и профиль research"

# ─────────────────────────────────────────────────────────────
step "ШАГ 4. Профиль Research — тот же проект, read-only"

reset_tree
ok "дерево возвращено к baseline: $(git status --porcelain | wc -l | tr -d ' ') изменений"

PROMPT_RES='Какие HTTP-эндпоинты есть в проекте и какие их ветки не покрыты тестами?'
say "Промпт: $PROMPT_RES"
say ""

( cd "$TASK" && claude -p "$PROMPT_RES" \
    --allowedTools "Read,Glob,Grep,Task" \
    < /dev/null ) 2>&1 | tee "$OUT/research-run.txt"

pause "главная проверка профиля research"

# ─────────────────────────────────────────────────────────────
step "ШАГ 5. Доказательство, что research ничего не тронул"

CHANGES=$(git status --porcelain | wc -l | tr -d ' ')
git status --short
if [ "$CHANGES" = "0" ]; then
  ok "git status ЧИСТ — профиль research не изменил ни одного файла"
  say "Это не дисциплина агента, а следствие набора инструментов: у субагента research"
  say "нет Edit, Write и Bash, поэтому изменить репозиторий он физически не мог."
else
  bad "обнаружено изменений: $CHANGES — профиль нарушил read-only"
fi

# ─────────────────────────────────────────────────────────────
step "ИТОГ"

echo "  Симптом до починки .............. HTTP $CODE_BEFORE  (ожидался 404)"
echo "  Симптом после bug-fix ........... HTTP $CODE_AFTER"
echo "  Изменений после research ........ $CHANGES"
printf '\n  Артефакты: %s\n' "$OUT"
ls -1 "$OUT" | sed 's/^/    /'
printf '\n'
