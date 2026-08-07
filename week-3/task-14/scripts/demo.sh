#!/bin/bash
# Показ Дня 14 для видео.
#
#   bash week-3/task-14/scripts/demo.sh        шаг по Enter
#   bash week-3/task-14/scripts/demo.sh -t     по таймеру
#   bash week-3/task-14/scripts/demo.sh -n     без живых прогонов
#
# Живые шаги идут в настоящий API. Нужен ANTHROPIC_API_KEY в окружении или .env.
# Шаг 8 дополнительно требует поднятого gateway Дня 13 на :8787 и пропускается,
# если его нет.

set -u
TASK="week-3/task-14"
TIMER=0; SPEED=1; RUN=1
while getopts "ts:n" opt; do case "$opt" in
    t) TIMER=1;; s) SPEED="$OPTARG";; n) RUN=0;; *) exit 2;; esac; done

B=$'\033[1m'; D=$'\033[2m'; R=$'\033[0m'
say() { printf '\n%s%s%s\n' "$B" "$1" "$R"; }
note() { printf '%s%s%s\n' "$D" "$1" "$R"; }
pause() {
    if [ "$TIMER" = "1" ]; then s=$(awk -v x="$SPEED" 'BEGIN{printf "%.1f",6*x}'); note "  … $s с"; sleep "$s"
    else printf '%s  [Enter]%s' "$D" "$R"; read -r _ || true; fi
}

clear
say "День 14. Security step"
note "Цикл: задача → генерация кода → компиляция и vet → security review → коммит."
note "Три задачи, каждая намеренно провоцирует небезопасный код."
pause

say "1. Три задачи"
cat "$TASK/dataset/tasks.jsonl" | python3 -c "
import json,sys
for l in sys.stdin:
    if not l.strip(): continue
    t=json.loads(l)
    print(f\"  {t['id']}\"); print('    ', t['task'][:110]); print('     провоцирует:', ', '.join(t['provokes']))
"
pause

say "2. Почему вердикт — перечисление, а не текст"
note "Модель возвращает одно из пяти значений: critical, high, medium, low, clean."
note "Решение о коммите принимает код по этому полю, а не по прозе ревью."
note ""
note "Это не защита от уговоров. Убедить модель написать «это тестовый код,"
note "всё нормально» можно — завтра партнёр будет это делать, пункт есть в задании."
note "Но превратить фразу в разрешение на коммит нельзя, пока severity = high."
pause

say "3. Коммит невозможно обойти"
note "Функция Commit принимает вердикт аргументом и отвергает его отсутствие"
note "наравне с блокирующим уровнем. Обходить нечего: пути в обход не существует."
note ""
note "Это ответ на спор в чате — сабагента можно не вызвать, инструмент обойти"
note "через shell. Ответ был «детерминированные инструменты». Вот они."
pause

say "4. План прогона"
if [ "$RUN" = "1" ]; then go run ./$TASK/cmd/loop -dry-run || true; else note "(пропущено -n)"; fi
pause

say "5. Прогон трёх задач"
if [ "$RUN" = "1" ]; then go run ./$TASK/cmd/loop -note "показ для видео" || true; else note "(пропущено -n)"; fi
pause

say "6. Что коммитнулось"
if [ "$RUN" = "1" ]; then find "$TASK/workspace/committed" -name '*.go' 2>/dev/null | head; else note "(пропущено -n)"; fi
pause

say "7. Попытка обойти ревью"
note "Флаг -skip-review пропускает security step. Смотрим, что будет с коммитом."
if [ "$RUN" = "1" ]; then
    go run ./$TASK/cmd/loop -only t1-token -rounds 1 -skip-review -note "обход" 2>/dev/null | sed -n '/Итог по задачам/,/^$/p' || true
else note "(пропущено -n)"; fi
pause

say "8. Весь цикл через gateway Дня 13"
note "Оба вызова — генерация и ревью — идут через прокси подменой base-url."
if [ "$RUN" = "1" ] && curl -sf -m 2 http://localhost:8787/health >/dev/null 2>&1; then
    go run ./$TASK/cmd/loop -base-url http://localhost:8787 -only t1-token -rounds 2 \
        -note "через gateway" 2>/dev/null | sed -n '/Итог по задачам/,/^$/p' || true
    note ""
    note "Счётчики гейтвея:"
    curl -s localhost:8787/stats || true; echo
else note "(пропущено: нужен снятый -n и поднятый gateway на :8787)"; fi
pause

say "9. Вывод"
note "Security step и gateway смотрят в разные стороны и почти не пересекаются."
note "Gateway защищает от того, что секрет уедет В модель."
note "Security step — от небезопасного кода, пришедшего ИЗ модели."
note ""
note "Поэтому колонка «поймал gateway» почти пустая, и это не провал, а следствие"
note "устройства. Интереснее третья колонка — что пропустили оба."
pause

say "Готово"
note "Разбор — в $TASK/README.md, контракт замера — в $TASK/docs/security-step.md"
