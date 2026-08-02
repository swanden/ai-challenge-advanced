#!/usr/bin/env python3
"""Готовит данные для дообучения из датасета Дня 6.

Собирает два набора, по одному на каждый вариант обучения:

    data/full/    — восемь классов, ровно как в Дне 6, без переделки
    data/binary/  — два ответа, yes или no: меняет ли задача публичный контракт

Формат — тот, что ждёт mlx_lm.lora: каталог с train.jsonl и valid.jsonl,
каждая строка — объект с полем messages. Дополнительно кладётся test.jsonl,
идентичный valid.jsonl: mlx использует его для флага --test.

Валидационная выборка берётся из train, а не из eval. Это принципиально:
eval за всю неделю ни разу не использовался для настройки чего-либо, только
для замеров, и должен остаться отложенным. Если подсматривать в него во
время обучения, итоговая цифра перестанет что-либо значить.

Запуск из корня репозитория:

    python3 week-2/finetune/scripts/prepare.py
"""

import json
import pathlib
import sys

ROOT = pathlib.Path(__file__).resolve().parents[3]
SRC = ROOT / "week-2" / "task-6" / "dataset"
OUT = ROOT / "week-2" / "finetune" / "data"

# Системный промпт для бинарной задачи. Он намеренно короче и конкретнее
# промпта Дня 9: там мы пытались объяснить различие словами и примерами, и
# День 9 показал, что промптом оно не держится — модель начинала видеть
# контракт всюду, где упоминался код статуса. Здесь объяснение убрано
# сознательно: различие должно оказаться в весах, а не в тексте запроса.
# Если оставить в промпте подробное описание, замер снова будет измерять
# качество промпта, а не результат обучения.
BINARY_SYSTEM = (
    "Ты определяешь, изменит ли задача публичный контракт сервиса notes-api. "
    "Ответь ровно одним словом: yes или no."
)

# Доля train, уходящая в валидацию. Валидация нужна, чтобы видеть
# переобучение: при шестидесяти четырёх примерах оно почти неизбежно, и
# момент, когда потеря на валидации пойдёт вверх при падающей потере на
# обучении, — это и есть сигнал остановиться.
VALID_EVERY = 6  # каждый шестой пример


def load(path):
    out = []
    for i, line in enumerate(path.read_text(encoding="utf-8").splitlines(), 1):
        line = line.strip()
        if not line:
            continue
        try:
            rec = json.loads(line)
        except json.JSONDecodeError as e:
            sys.exit(f"{path} строка {i}: {e}")
        msgs = rec.get("messages", [])
        if len(msgs) != 3:
            sys.exit(f"{path} строка {i}: ожидалось 3 сообщения")
        out.append(msgs)
    if not out:
        sys.exit(f"{path}: пусто")
    return out


def to_binary(msgs):
    """Переразмечает пример под бинарную задачу."""
    answer = "yes" if msgs[2]["content"] == "contract-change" else "no"
    return [
        {"role": "system", "content": BINARY_SYSTEM},
        {"role": "user", "content": msgs[1]["content"]},
        {"role": "assistant", "content": answer},
    ]


def write(path, records):
    path.parent.mkdir(parents=True, exist_ok=True)
    with path.open("w", encoding="utf-8") as f:
        for r in records:
            f.write(json.dumps({"messages": r}, ensure_ascii=False) + "\n")


def split(records):
    """Делит на обучение и валидацию детерминированно, как в Дне 6."""
    train, valid = [], []
    for i, r in enumerate(records):
        (valid if i % VALID_EVERY == VALID_EVERY - 1 else train).append(r)
    return train, valid


def describe(name, train, valid, key):
    counts = {}
    for r in train + valid:
        counts[key(r)] = counts.get(key(r), 0) + 1
    parts = ", ".join(f"{k} {v}" for k, v in sorted(counts.items()))
    print(f"{name}: обучение {len(train)}, валидация {len(valid)} | {parts}")


def main():
    train_src = load(SRC / "train.jsonl")
    eval_src = load(SRC / "eval.jsonl")

    # Вариант 1: восемь классов, без переделки.
    tr, va = split(train_src)
    write(OUT / "full" / "train.jsonl", tr)
    write(OUT / "full" / "valid.jsonl", va)
    write(OUT / "full" / "test.jsonl", va)
    describe("восемь классов", tr, va, lambda r: r[2]["content"])

    # Вариант 2: бинарный вопрос.
    bin_all = [to_binary(m) for m in train_src]
    btr, bva = split(bin_all)
    write(OUT / "binary" / "train.jsonl", btr)
    write(OUT / "binary" / "valid.jsonl", bva)
    write(OUT / "binary" / "test.jsonl", bva)
    describe("бинарный вопрос", btr, bva, lambda r: r[2]["content"])

    # Отложенный набор для итогового замера, в обучении не участвует.
    write(OUT / "holdout" / "full.jsonl", eval_src)
    write(OUT / "holdout" / "binary.jsonl", [to_binary(m) for m in eval_src])
    print(f"отложенный набор: {len(eval_src)} примеров, в обучении не участвует")

    # Перекос бинарной задачи стоит назвать числом: если ответов no
    # семь восьмых, модель может научиться отвечать no всегда и получить
    # 88% точности при нулевой пользе. Это проверяется отдельно.
    yes = sum(1 for r in bin_all if r[2]["content"] == "yes")
    print(f"перекос бинарной задачи: yes {yes}, no {len(bin_all) - yes} "
          f"({100 * (len(bin_all) - yes) / len(bin_all):.0f}% ответов no)")


if __name__ == "__main__":
    main()
