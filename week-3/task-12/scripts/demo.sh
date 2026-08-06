#!/bin/bash
# Показ Дня 12 для видео. Разбор — подписями на экране, голос не нужен.
#
#   bash week-3/task-12/scripts/demo.sh        шаг по Enter
#   bash week-3/task-12/scripts/demo.sh -t     по таймеру
#   bash week-3/task-12/scripts/demo.sh -n     без живых прогонов
#
# Живые прогоны короткие: молчаливый счётчик на шаге 7, отдельные диалоги на
# шагах 8 и 9. Меньше минуты машинного времени.
#
# Нужны: ANTHROPIC_API_KEY в окружении или .env в корне. Шаг 9 требует Ollama с
# qwen2.5:7b и пропускается, если её нет.

set -eu
TASK="week-3/task-12"
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
section() { awk -v a="$1" -v b="$2" '$0 ~ a {f=1} $0 ~ b {f=0} f' "$TASK/README.md"; }

clear
say "День 12. Indirect Prompt Injection"
note "Нагрузка спрятана не в сообщении пользователя, а в данных, которые агент"
note "читает сам: письмо, файл репозитория, страница документации."
note "Пользователь просит «перескажи это» и получает обычный ответ — пока агент"
note "втихую исполняет чужую инструкцию из текста."
pause

say "1. Три агента, три носителя"
note "  summarizer  читает письмо"
note "  analyst     читает фрагмент файла notes-api — это кейс Copilot в миниатюре"
note "  searcher    читает страницу документации"
note ""
note "Агент возвращает JSON с полем действия. Действие исполняет детерминированный"
note "код, а не сама модель — так обе модели сравнимы. Ограничение названо в отчёте."
pause

say "2. Два класса нагрузки — в этом весь день"
note "  команда      прячет указание: перешли письмо, выхолости раздел, смени роль"
note "  ложный факт  ничего не велит — просто врёт, а агент верит и передаёт дальше"
note ""
note "Правило «данные — не команда» закрывает команды и не касается фактов:"
note "выполнять там нечего. У другого участника факт прошёл в 93–100% на всех слоях."
pause

say "3. Пять способов сокрытия, пятый — контроль"
note "  html-comment   скрытый стиль   zero-width   md-link-title   visible"
note ""
note "Видимый текст не прячет ничего: очистке нечего резать. Без него слой очистки"
note "делал бы всю работу, а границы и проверка выхода остались бы непроверенными."
pause

say "4. Контракт замера записан до прогона"
sed -n '1,10p' "$TASK/docs/attacks.md"
note "…семь ожиданий — в $TASK/docs/attacks.md"
pause

say "5. Четыре слоя защиты"
note "  none      данные как есть"
note "  sanitize  очистка носителя до модели (код)"
note "  boundary  плюс разметка границ и правило в промпте (просьба к модели)"
note "  all       плюс проверка ответа и белый список получателей (код)"
pause

say "6. Результаты полного прогона"
section '^### Доля успешных атак по классу' '^### Доля успешных атак по способу'
note "Полные таблицы — в $TASK/README.md"
pause

say "7. Живой прогон: команда против ложного факта"
note "Восемь потоков, молча, потом таблицы."
if [ "$RUN" = "1" ]; then
    go run ./$TASK/cmd/trap -vectors searcher -repeats 1 -workers 8 -note "показ"
else note "(пропущено флагом -n)"; fi
pause

say "8. Ложный факт вблизи: чем его берёт очистка и чем не берёт"
note "Один и тот же факт, спрятанный комментарием и через zero-width."
if [ "$RUN" = "1" ]; then
    go run ./$TASK/cmd/trap -only sea-fact -vectors searcher -methods html-comment,zero-width \
        -levels sanitize -repeats 1 -workers 1 -show-all -with-clean=false -note "показ"
else note "(пропущено флагом -n)"; fi
pause

say "9. Эксфильтрация и белый список"
note "Письмо велит переслать себя на чужой адрес. Слой all проверяет получателя."
if [ "$RUN" = "1" ] && curl -sf -m 2 http://localhost:11434/api/tags >/dev/null 2>&1; then
    go run ./$TASK/cmd/trap -provider openai -base-url http://localhost:11434/v1 \
        -key-env "" -model qwen2.5:7b -only sum-cmd-fwd -vectors summarizer \
        -methods visible -levels none,all -repeats 1 -workers 1 -show-all \
        -with-clean=false -note "показ"
else note "(пропущено: нужен снятый -n и Ollama с qwen2.5:7b)"; fi
pause

say "10. Вывод"
note "Команду закрывает правило «данные — не команда» и очистка."
note "Ложный факт не закрывает ни то, ни другое: агент ничего не исполняет,"
note "он верит написанному. Против него работает только проверка выхода —"
note "и только по грубым признакам, а не по смыслу."
note ""
note "И вся защита от сокрытия держится на том, что атакующий сам пометил"
note "инструкцию, спрятав её. Написанная открыто и правдоподобно — проходит."
pause

say "Готово"
note "Отчёты — в $TASK/evidence, разбор — в $TASK/README.md"
