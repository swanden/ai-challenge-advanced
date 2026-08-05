#!/bin/bash
# Показ Дня 11 для видео. Весь разбор — подписями на экране, голосом
# комментировать ничего не нужно.
#
# Запускать из корня репозитория:
#
#   bash week-3/task-11/scripts/demo.sh           шаг по Enter
#   bash week-3/task-11/scripts/demo.sh -t        по таймеру
#   bash week-3/task-11/scripts/demo.sh -t -s 1.5 таймер медленнее
#   bash week-3/task-11/scripts/demo.sh -n        без живых прогонов
#
# Полные прогоны здесь не запускаются: их четыре, по 768 вызовов, и на видео
# это несколько минут ожидания. Их результаты показываются готовыми таблицами
# из README, а вживую идут три коротких запуска — тот минимум, который
# показывает, что за таблицами стоит работающий код.
#
# Порядок такой: сначала цифры, потом взгляд вблизи. Шаг 7 считает молча в
# восемь потоков и печатает таблицы — около двадцати секунд. Шаги 8 и 9
# показывают отдельные диалоги целиком, вызовов там единицы.
#
# Печать сама по себе времени не стоит; стоит его параллелизм. Читаемый поток
# диалогов требует одного-двух потоков, и шаг 7 на них растянулся бы до
# полутора минут ради кадра, который лучше показан в шагах 8 и 9.
#
# В шагах 8 и 9 контрольный набор отключён: он там ничего не показывает, а
# времени и экрана занимает больше самих атак.
#
# Итого машинного времени — меньше минуты.
#
# Нужны: ANTHROPIC_API_KEY в окружении или в .env в корне. Шаг 9 дополнительно
# требует поднятой Ollama с qwen2.5:7b и пропускается, если её нет.

set -eu

TASK="week-3/task-11"
TIMER=0
SPEED=1
RUN=1

while getopts "ts:n" opt; do
    case "$opt" in
        t) TIMER=1 ;;
        s) SPEED="$OPTARG" ;;
        n) RUN=0 ;;
        *) echo "неизвестный флаг" >&2; exit 2 ;;
    esac
done

B=$'\033[1m'; D=$'\033[2m'; R=$'\033[0m'

say() { printf '\n%s%s%s\n' "$B" "$1" "$R"; }
note() { printf '%s%s%s\n' "$D" "$1" "$R"; }

pause() {
    if [ "$TIMER" = "1" ]; then
        local secs
        secs=$(awk -v s="$SPEED" 'BEGIN{printf "%.1f", 6*s}')
        note "  … $secs с"
        sleep "$secs"
    else
        printf '%s  [Enter]%s' "$D" "$R"
        read -r _ || true
    fi
}

# Печатает кусок README между двумя заголовками.
section() {
    awk -v a="$1" -v b="$2" '$0 ~ a {f=1} $0 ~ b {f=0} f' "$TASK/README.md"
}

clear

say "День 11. Prompt Injection"
note "Атакуем два собственных системных промпта и меряем, что из защиты работает."
note "Мишени: помощник банка из задания и классификатор задач из Дней 6–10."
pause

say "1. Почему мишеней две"
note "У банка выход — свободный текст. «Атака прошла» там надёжно ловится только"
note "канарейкой: внутрь промпта вписан выдуманный партнёрский ключ."
note ""
note "У классификатора выход — enum из восьми классов. Там исход считается точно:"
note "ответ либо ровно один класс, либо нет; класс либо заказанный атакой, либо нет."
note ""
note "Ломать в классификаторе нечего — ни секретов, ни полномочий. Он взят не как"
note "жертва, а как измерительный прибор."
pause

say "2. Контракт замера записан до прогона"
sed -n '1,12p' "$TASK/docs/attacks.md"
note "…восемь ожиданий и слабости детекторов — в $TASK/docs/attacks.md"
pause

say "3. Набор атак"
python3 - "$TASK/dataset/attacks.jsonl" <<'PY'
import json, sys, collections
rows = [json.loads(l) for l in open(sys.argv[1], encoding="utf-8") if l.strip()]
print(f"  {len(rows)} записей")
for k, v in collections.Counter(r["technique"] for r in rows).most_common():
    print(f"    {k:<12} {v}")
print("  по типу: " + ", ".join(
    f"{k} {v}" for k, v in collections.Counter(r["type"] for r in rows).most_common()))
PY
note ""
note "Две записи перенесены из Дня 7 дословно. Одна из них — noisy-04 — прошла"
note "там все четыре слоя контроля уверенности с вероятностью 1.00."
pause

say "4. Три техники, которые требует задание"
python3 - "$TASK/dataset/attacks.jsonl" <<'PY'
import json, sys
want = {"ext-01": "system prompt extraction",
        "rp-01": "role-play injection",
        "ovr-01": "instruction override"}
for line in open(sys.argv[1], encoding="utf-8"):
    if not line.strip():
        continue
    a = json.loads(line)
    if a["id"] in want:
        print(f"\n  [{want[a['id']]}]  {a['id']}")
        print("  " + a["text"][:190])
PY
pause

say "5. Четыре слоя защиты, накопительные"
note "  none        промпт-жертва: роль, секреты и строчка «не раскрывай инструкции»"
note "  hardened    плюс блок правил"
note "  delimiters  плюс разделители вокруг пользовательского текста"
note "  all         плюс детерминированные фильтры входа и выхода"
note ""
note "Слои разложены по отдельности, а не «до и после»: иначе неизвестно,"
note "что именно сработало."
pause

say "6. Результаты четырёх полных прогонов"
section '^### Доля успешных атак по слоям защиты' '^### По технике'
note "Полные таблицы по технике и цена защиты — в $TASK/README.md"
pause

say "7. Живой прогон, чтобы за таблицами был виден работающий код"
note "Классификатор, промпт-жертва против полного набора фильтров."
note "Восемь потоков, около двадцати секунд — на экране счётчик, потом таблицы."
if [ "$RUN" = "1" ]; then
    go run ./$TASK/cmd/inject -targets router -defenses none,all \
        -repeats 1 -workers 8 -note "показ для видео"
else
    note "(пропущено флагом -n)"
fi
pause

say "8. Теперь то же самое вблизи: три требуемые техники на своём промпте"
note "Вход и ответ модели целиком, промпт-жертва против упрочнённого."
note "Это и есть «скриншоты атак», которых просит задание."
if [ "$RUN" = "1" ]; then
    go run ./$TASK/cmd/inject -only ext-01,rp-01,ovr-01 -targets bank \
        -defenses none,hardened -repeats 1 -workers 1 -show-all \
        -with-clean=false -note "расшифровка для видео"
else
    note "(пропущено флагом -n)"
fi
pause

say "9. Единственная атака, пережившая все четыре слоя"
note "hij-02 подменяет contract-change на refactor. Полный набор фильтров включён."
note "Выходной фильтр её пропускает — потому что refactor валидный класс."
if [ "$RUN" = "1" ] && curl -sf -m 2 http://localhost:11434/api/tags >/dev/null 2>&1; then
    go run ./$TASK/cmd/inject -provider openai \
        -base-url http://localhost:11434/v1 -key-env "" -model qwen2.5:7b \
        -only hij-02 -targets router -defenses all -repeats 1 -workers 1 \
        -show-all -with-clean=false -note "показ для видео"
else
    note "(пропущено: нужен -n снят и поднятая Ollama с qwen2.5:7b)"
fi
pause

say "10. Вывод"
note "Ноль успешных атак у банка на упрочнённых слоях и ноль у классификатора"
note "при all — это разные нули."
note ""
note "Первый обеспечен тем, как повела себя конкретная модель. Второй — восемью"
note "строками whitelist'а, которые верны для любой модели: ответ не из списка"
note "наружу не выходит."
note ""
note "Защищаемость задаётся формой выхода, а не текстом промпта."
note ""
note "А переживает всё только подмена класса — потому что её результат"
note "неотличим от правильного. Фильтр умеет сказать «такого ответа не бывает»"
note "и не умеет сказать «такой бывает, но здесь он неверен»."
pause

say "Готово"
note "Отчёты — в $TASK/evidence, разбор — в $TASK/README.md"
