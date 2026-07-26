#!/usr/bin/env bash
# Метрики прогона + независимая проверка ворот на каждом коммите.
# Запуск ИЗ КОРНЯ репозитория: bash week-1/task-5/scripts/report.sh <номер прогона>
set -uo pipefail

RUN="${1:-1}"
TASK="week-1/task-5/notes-api"
J="$TASK/runs/run-$RUN/tasks.jsonl"

[ -f "$J" ] || { echo "нет журнала: $J"; exit 1; }

echo "# Прогон execution loop №$RUN"
echo
python3 - "$J" <<'PY'
import json, sys, statistics
rows = [json.loads(l) for l in open(sys.argv[1], encoding='utf-8') if l.strip()]
done = [r for r in rows if r.get('verdict') == 'DONE']
failed = [r for r in rows if r.get('verdict') == 'FAILED']

streak = 0
for r in rows:
    if r.get('verdict') == 'DONE':
        streak += 1
    else:
        break

secs = [r.get('seconds', 0) for r in rows if r.get('seconds')]
first_try = [r for r in done if r.get('gate_attempts', 1) == 1]

print("## Метрики\n")
print("| метрика | значение |")
print("|---|---|")
print(f"| **задач подряд без вмешательства (streak)** | **{streak} из {len(rows)}** |")
print(f"| выполнено всего | {len(done)} из {len(rows)} |")
print(f"| провалено | {len(failed)} |")
if done:
    print(f"| прошли ворота с первой попытки | {len(first_try)} из {len(done)} "
          f"({round(100*len(first_try)/len(done))}%) |")
if secs:
    print(f"| среднее время на задачу | {round(statistics.mean(secs))} с |")
    print(f"| медиана | {round(statistics.median(secs))} с |")
    print(f"| самая долгая задача | {max(secs)} с |")
    print(f"| суммарное время по задачам | {round(sum(secs)/60)} мин |")
print()
print("## Задачи\n")
print("| # | id | тип | профиль | вердикт | время | ворот | коммит | заметка |")
print("|---|----|-----|---------|---------|-------|-------|--------|---------|")
for i, r in enumerate(rows, 1):
    print(f"| {i} | {r.get('id','')} | {r.get('type','')} | {r.get('profile','')} "
          f"| {r.get('verdict','')} | {r.get('seconds','')}с | {r.get('gate_attempts','')} "
          f"| `{r.get('commit','')}` | {r.get('note','')} |")
if failed:
    print()
    print("## Где сломался\n")
    for r in failed:
        print(f"- **{r.get('id')}** ({r.get('type')}): {r.get('note') or 'причина не записана'}")
PY

echo
echo "## Независимая проверка ворот"
echo
echo "| коммит | build | vet | gofmt | test |"
echo "|---|---|---|---|---|"
HEAD_NOW=$(git rev-parse --abbrev-ref HEAD)
for c in $(python3 -c "
import json,sys
for l in open('$J',encoding='utf-8'):
    if l.strip():
        r=json.loads(l)
        if r.get('commit'): print(r['commit'])
"); do
  git checkout -q "$c" 2>/dev/null || { echo "| \`$c\` | нет такого коммита | | | |"; continue; }
  b=$(go build ./$TASK/... 2>&1 >/dev/null && echo ok || echo FAIL)
  v=$(go vet   ./$TASK/... 2>&1 >/dev/null && echo ok || echo FAIL)
  f=$([ -z "$(gofmt -l week-1/task-5 2>/dev/null)" ] && echo ok || echo FAIL)
  t=$(go test  ./$TASK/... -count=1 >/dev/null 2>&1 && echo ok || echo FAIL)
  echo "| \`$c\` | $b | $v | $f | $t |"
done
git checkout -q "$HEAD_NOW"
echo
echo "Проверка прогнана независимо от агента: вердикты в таблице выше — по фактическому выводу команд."
