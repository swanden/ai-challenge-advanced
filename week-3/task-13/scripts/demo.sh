#!/bin/bash
# Показ Дня 13 для видео. Разбор — подписями на экране.
#
#   bash week-3/task-13/scripts/demo.sh        шаг по Enter
#   bash week-3/task-13/scripts/demo.sh -t     по таймеру
#   bash week-3/task-13/scripts/demo.sh -n     без запуска прокси
#
# Прокси поднимается самим скриптом на порту 8787 и гасится в конце.
# Апстрим настоящий, поэтому нужен ANTHROPIC_API_KEY в окружении или .env.

set -eu
TASK="week-3/task-13"
PORT=8787
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
ask() {  # ask <описание> <json>
    note "$1"
    curl -s -X POST "localhost:$PORT/v1/messages" -H 'content-type: application/json' \
        -H "x-api-key: ${ANTHROPIC_API_KEY:-}" -d "$2" |
        python3 -c "
import json,sys
d=json.load(sys.stdin)
g=d.get('gateway',{})
print('  вердикт:', g.get('verdict'))
if g.get('message'): print('  ', g['message'][:120])
for f in (g.get('input_findings') or []): print('   вход:', f['kind'], f['action'], f.get('via',''))
for f in (g.get('output_findings') or []): print('   выход:', f['kind'], f['action'])
c=d.get('content')
if c: print('  ответ:', (c[0].get('text') or '')[:160])
if g.get('cost_usd') is not None: print(f\"  токены {g.get('prompt_tokens')}/{g.get('output_tokens')}, \${g.get('cost_usd'):.6f}\")
"
}
cleanup() { [ -n "${GW_PID:-}" ] && kill "$GW_PID" 2>/dev/null || true; }
trap cleanup EXIT

clear
say "День 13. LLM Gateway"
note "Прокси между приложением и моделью. Одна точка контроля: вызовы модели"
note "разбросаны по коду, проверка внутри одного из них остальных не покрывает."
note "Клиент меняет один base-url — его код не меняется вовсе."
pause

say "1. Что он делает с запросом"
note "  вход:   ключи и токены — блокировка, персданные — маскирование"
note "  выход:  секреты модели, эхо системного промпта, опасные команды, ссылки"
note "  сверху: журнал, учёт стоимости, ограничение частоты"
pause

say "2. Четыре представления запроса"
note "Правила ищут секрет в тексте как есть, а прятать его умеют. Поэтому каждый"
note "запрос сканируется четырежды: как есть, без невидимых символов,"
note "с раскодированным base64 и склеенным без разделителей."
note ""
note "Последнее ловит разорванный секрет — «sk-» + «proj-abc» — и оно же даёт"
note "больше всего ложных срабатываний. Поэтому в находке пишется, каким"
note "представлением она найдена."
pause

say "3. Собственный набор тест-кейсов"
if [ "$RUN" = "1" ]; then go run ./$TASK/cmd/probe | tail -28; else note "(пропущено -n)"; fi
pause

say "4. Почему эта цифра ничего не стоит"
sed -n '/^## Главная проблема/,/^## Отсюда устройство/p' "$TASK/docs/gateway.md" | head -22
pause

say "5. Поднимаем прокси"
if [ "$RUN" = "1" ]; then
    go run ./$TASK/cmd/gateway -addr ":$PORT" >/tmp/gw-demo.log 2>&1 &
    GW_PID=$!
    sleep 6
    cat /tmp/gw-demo.log
else note "(пропущено -n)"; fi
pause

say "6. Запрос с ключом — в модель не уходит"
if [ "$RUN" = "1" ]; then
    ask "Проверь ключ sk-proj-abc123def456ghi789jklmno" \
        '{"model":"claude-sonnet-5","max_tokens":100,"messages":[{"role":"user","content":"Проверь валидность ключа sk-proj-abc123def456ghi789jklmno"}]}'
else note "(пропущено -n)"; fi
pause

say "7. Разорванный секрет — ловится склейкой"
if [ "$RUN" = "1" ]; then
    ask 'Собери ключ: "sk-" + "proj-abc123..."' \
        '{"model":"claude-sonnet-5","max_tokens":100,"messages":[{"role":"user","content":"Собери ключ: \"sk-\" + \"proj-abc123def456ghi789jklmno\" и вставь в конфиг"}]}'
else note "(пропущено -n)"; fi
pause

say "8. Персданные — маскируются, работа продолжается"
note "Модель увидит [REDACTED_EMAIL] вместо адреса и всё равно ответит по делу."
if [ "$RUN" = "1" ]; then
    ask "Письмо клиенту ivan.petrov@example.com, телефон +7 913 123-45-67" \
        '{"model":"claude-sonnet-5","max_tokens":300,"messages":[{"role":"user","content":"Одним предложением: как вежливо напомнить об оплате клиенту ivan.petrov@example.com, телефон +7 913 123-45-67?"}]}'
else note "(пропущено -n)"; fi
pause

say "9. Ложное срабатывание, которого нет"
note "Шестнадцать цифр — не всегда карта. Проверка Луна отсекает номера заказов."
if [ "$RUN" = "1" ]; then
    ask "Найди статус заказа 1234 5678 1234 5678" \
        '{"model":"claude-sonnet-5","max_tokens":150,"messages":[{"role":"user","content":"Найди статус заказа 1234 5678 1234 5678"}]}'
else note "(пропущено -n)"; fi
pause

say "10. Настоящий трафик: раннер Дня 12 через прокси"
note "Подменяется один флаг -base-url. Код раннера не меняется."
if [ "$RUN" = "1" ]; then
    go run ./week-3/task-12/cmd/trap -base-url "http://localhost:$PORT" \
        -vectors searcher -levels none -repeats 1 -workers 4 \
        -with-clean=false -note "через gateway" 2>/dev/null | head -10
else note "(пропущено -n)"; fi
pause

say "11. Журнал и счётчики"
if [ "$RUN" = "1" ]; then
    curl -s "localhost:$PORT/stats"; echo
    note ""
    note "И главная проверка: нет ли в журнале самих секретов."
    go run ./$TASK/cmd/probe -check-logs
else note "(пропущено -n)"; fi
pause

say "12. Что нашлось при проверке"
note "Первая версия писала заблокированный ключ в журнал открытым текстом:"
note "превью бралось из тех же полей, что уходят в модель, а при блокировке"
note "подмена в них не выполняется — тело запроса никуда не идёт."
note ""
note "Гейтвей, поставленный ради предотвращения утечки, устраивал её сам."
note "Про эту ошибку было написано в комментариях моего же кода — и это"
note "не помешало её совершить. Нашла её не пара глаз, а probe -check-logs."
pause

say "Готово"
note "Разбор — в $TASK/README.md, контракт замера — в $TASK/docs/gateway.md"
