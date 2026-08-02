#!/bin/bash
# Полный цикл дообучения: подготовка данных, два обучения, четыре замера.
#
# Запускать из корня репозитория:
#
#   bash week-2/finetune/scripts/run.sh
#
# Флаги:
#   -i N   число итераций обучения (по умолчанию 200)
#   -p     только подготовка данных и замеры базовой модели, без обучения

set -eu

ITERS=200
TRAIN=1
MODEL="mlx-community/Qwen2.5-7B-Instruct-4bit"
ROOT="week-2/finetune"

while getopts "i:p" opt; do
    case "$opt" in
        i) ITERS="$OPTARG" ;;
        p) TRAIN=0 ;;
        *) echo "неизвестный флаг" >&2; exit 2 ;;
    esac
done

command -v mlx_lm.lora >/dev/null 2>&1 || {
    echo "нет mlx_lm.lora — установите: pip install mlx-lm" >&2
    exit 1
}

echo "── подготовка данных ──"
python3 "$ROOT/scripts/prepare.py"

echo
echo "── замер ДО обучения ──"
python3 "$ROOT/scripts/evaluate.py" --task full --model "$MODEL" --note "базовая, 8 классов"
python3 "$ROOT/scripts/evaluate.py" --task binary --model "$MODEL" --note "базовая, бинарный вопрос"

if [ "$TRAIN" = "0" ]; then
    echo "обучение пропущено флагом -p"
    exit 0
fi

for task in full binary; do
    echo
    echo "── обучение: $task, $ITERS итераций ──"
    # Каталог адаптера чистится перед обучением. Без этого промежуточные
    # срезы от прошлого прогона остаются на месте, и шаг замера срезов
    # ниже измеряет их, выдавая за результат текущего обучения. Ошибка
    # тихая: цифры выглядят правдоподобно, потому что это настоящие
    # цифры — просто от другого прогона.
    rm -rf "$ROOT/adapters/$task" "$ROOT/adapters/$task-100"
    # --num-layers 8 вместо 16 по умолчанию: примеров мало, и чем меньше
    # обучаемых слоёв, тем меньше шансов, что модель заучит выборку
    # вместо того, чтобы чему-то научиться.
    mlx_lm.lora \
        --model "$MODEL" \
        --train \
        --data "$ROOT/data/$task" \
        --adapter-path "$ROOT/adapters/$task" \
        --iters "$ITERS" \
        --batch-size 1 \
        --num-layers 8 \
        --steps-per-eval 25 \
        2>&1 | tee "$ROOT/evidence/train-$task.log"
done

echo
echo "── замер ПОСЛЕ обучения ──"
# Меряются и финальные веса, и промежуточный срез. Потеря на валидации
# в обоих обучениях достигает минимума задолго до конца и дальше растёт —
# то есть финальные веса переобучены, а срез на сотой итерации может
# оказаться лучше. Он уже сохранён, замер бесплатный.
python3 "$ROOT/scripts/evaluate.py" --task full \
    --model "$MODEL" --adapter "$ROOT/adapters/full" --note "дообученная, 8 классов"
python3 "$ROOT/scripts/evaluate.py" --task binary \
    --model "$MODEL" --adapter "$ROOT/adapters/binary" --note "дообученная, бинарный вопрос"

# Срезы сохраняются каждые сто итераций, поэтому при меньшем числе
# итераций их просто не будет — шаг тихо пропускается.
if [ "$ITERS" -gt 100 ]; then
echo
echo "── замер промежуточного среза (100 итераций) ──"
for task in full binary; do
    ckpt="$ROOT/adapters/$task/0000100_adapters.safetensors"
    if [ -f "$ckpt" ]; then
        tmp="$ROOT/adapters/$task-100"
        mkdir -p "$tmp"
        cp "$ckpt" "$tmp/adapters.safetensors"
        cp "$ROOT/adapters/$task/adapter_config.json" "$tmp/" 2>/dev/null || true
        python3 "$ROOT/scripts/evaluate.py" --task "$task" \
            --model "$MODEL" --adapter "$tmp" --note "срез на 100 итерациях, $task"
    fi
done
fi

echo
echo "готово. Отчёты в $ROOT/evidence/"