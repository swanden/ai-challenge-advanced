#!/usr/bin/env python3
"""Меряет модель на отложенном наборе: до дообучения и после.

Работает через mlx_lm напрямую, а не через Ollama: адаптер LoRA существует
как отдельный каталог с весами, и подключить его к Ollama без слияния и
конвертации нельзя. Поэтому и базовый замер здесь делается тем же способом —
иначе сравнивали бы разные среды исполнения.

Отсюда важная оговорка для отчёта: цифры базовой модели здесь могут не
совпасть с 63% из Дней 8–10, потому что там модель работала через Ollama в
квантовании GGUF, а тут — в формате MLX. Сравнивать нужно базовую и
дообученную внутри этого замера, а не через неделю.

Запуск из корня репозитория:

    python3 week-2/finetune/scripts/evaluate.py --task full
    python3 week-2/finetune/scripts/evaluate.py --task full --adapter week-2/finetune/adapters/full
    python3 week-2/finetune/scripts/evaluate.py --task binary --adapter week-2/finetune/adapters/binary
"""

import argparse
import collections
import json
import pathlib
import sys
import time

ROOT = pathlib.Path(__file__).resolve().parents[3]
DATA = ROOT / "week-2" / "finetune" / "data" / "holdout"
OUT = ROOT / "week-2" / "finetune" / "evidence"

DEFAULT_MODEL = "mlx-community/Qwen2.5-7B-Instruct-4bit"


def load_holdout(task):
    path = DATA / f"{task}.jsonl"
    if not path.exists():
        sys.exit(f"нет файла {path} — сначала запустите prepare.py")
    out = []
    for line in path.read_text(encoding="utf-8").splitlines():
        line = line.strip()
        if line:
            out.append(json.loads(line)["messages"])
    return out


def parse_answer(raw, allowed):
    """Достаёт ответ из текста модели.

    Возвращает пару: сам ответ и признак того, что он пришёл ровно одним
    словом без лишнего. Разбор многословного ответа засчитывается как
    нарушение формата, даже если ответ угадан верно, — так же, как во всех
    предыдущих днях.
    """
    s = raw.strip().lower().strip("`\"'.!,:; \n\t")
    if s in allowed:
        return s, True
    # Длинные имена проверяем раньше коротких.
    for a in sorted(allowed, key=len, reverse=True):
        if a in s:
            return a, False
    return "", False


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--task", choices=["full", "binary"], required=True)
    ap.add_argument("--model", default=DEFAULT_MODEL)
    ap.add_argument("--adapter", default=None, help="каталог с адаптером LoRA; без него — базовая модель")
    ap.add_argument("--max-tokens", type=int, default=16)
    ap.add_argument("--note", default="")
    args = ap.parse_args()

    try:
        from mlx_lm import generate, load
    except ImportError:
        sys.exit("нет mlx_lm: pip install mlx-lm")

    holdout = load_holdout(args.task)
    allowed = sorted({m[2]["content"] for m in holdout})
    print(f"задача {args.task}: {len(holdout)} примеров, допустимые ответы: {', '.join(allowed)}")

    kind = "дообученная" if args.adapter else "базовая"
    print(f"модель {args.model} ({kind})")
    load_kwargs = {}
    if args.adapter:
        load_kwargs["adapter_path"] = args.adapter
    model, tokenizer = load(args.model, **load_kwargs)

    results = []
    started = time.time()
    for i, msgs in enumerate(holdout, 1):
        prompt = tokenizer.apply_chat_template(
            msgs[:2], add_generation_prompt=True, tokenize=False
        )
        t0 = time.time()
        raw = generate(model, tokenizer, prompt=prompt, max_tokens=args.max_tokens, verbose=False)
        latency_ms = int((time.time() - t0) * 1000)

        expected = msgs[2]["content"]
        got, exact = parse_answer(raw, allowed)
        ok = got == expected
        results.append({
            "index": i,
            "user": msgs[1]["content"],
            "expected": expected,
            "raw": raw,
            "predicted": got,
            "correct": ok,
            "well_formed": exact,
            "latency_ms": latency_ms,
        })
        print(f"{'✓' if ok else '×'} {got or '—':16s} ожидалось {expected:16s} {raw.strip()[:40]!r}")

    correct = sum(1 for r in results if r["correct"])
    well = sum(1 for r in results if r["well_formed"])
    total = len(results)

    print(f"\nточность {100 * correct / total:.0f}% ({correct} из {total})")
    print(f"формат соблюдён в {100 * well / total:.0f}% ответов")
    print(f"средняя задержка {sum(r['latency_ms'] for r in results) // total} мс")

    # Разбивка по ожидаемому ответу: при бинарной задаче с перекосом семь
    # к одному общая точность обманчива — модель, отвечающая no всегда,
    # получит 88%. Полнота по каждому ответу это вскрывает.
    by = collections.defaultdict(lambda: [0, 0])
    for r in results:
        by[r["expected"]][1] += 1
        if r["correct"]:
            by[r["expected"]][0] += 1
    print("по ответам: " + ", ".join(f"{k} {v[0]}/{v[1]}" for k, v in sorted(by.items())))

    # Отдельно — не выродилась ли модель в один ответ.
    #
    # Первая версия этой проверки срабатывала, только если все ответы
    # совпали. Этого мало: на бинарной задаче с перекосом семь к одному
    # модель может ответить «нет» пятнадцать раз из шестнадцати, получить
    # 94% точности и при этом не находить то единственное, ради чего
    # обучалась. Порог в две трети ловит и такой случай.
    predicted = collections.Counter(r["predicted"] for r in results)
    top_answer, top_count = predicted.most_common(1)[0]
    if top_count / total >= 2 / 3:
        print(f"ВНИМАНИЕ: ответ {top_answer!r} дан в {top_count} случаях из {total} "
              f"({100 * top_count / total:.0f}%) — возможно вырождение, "
              f"смотрите полноту по ответам выше")

    # Полнота по редкому ответу важнее общей точности, когда классы
    # неравны: модель, отвечающая всегда одинаково, покажет высокую
    # точность и нулевую пользу.
    rarest = min(by.items(), key=lambda kv: kv[1][1])
    commonest = max(by.items(), key=lambda kv: kv[1][1])
    # Печатаем только при настоящем перекосе. На сбалансированной задаче,
    # где у каждого класса поровну примеров, понятия редкого ответа нет,
    # и строка вводила бы в заблуждение.
    if commonest[1][1] >= 3 * rarest[1][1]:
        got, tot = rarest[1]
        print(f"полнота по редкому ответу {rarest[0]!r}: {got}/{tot} "
              f"({100 * got / tot:.0f}%) — это и есть польза модели на этой задаче")

    OUT.mkdir(parents=True, exist_ok=True)
    name = f"{args.task}-{'tuned' if args.adapter else 'base'}-{time.strftime('%Y%m%d-%H%M%S')}.json"
    path = OUT / name
    path.write_text(json.dumps({
        "task": args.task,
        "model": args.model,
        "adapter": args.adapter,
        "note": args.note,
        "accuracy": correct / total,
        "format_compliance": well / total,
        "elapsed_s": round(time.time() - started, 1),
        "by_expected": {k: {"correct": v[0], "total": v[1]} for k, v in sorted(by.items())},
        "predicted_distribution": dict(predicted),
        "results": results,
    }, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    print(f"\nотчёт: {path}")


if __name__ == "__main__":
    main()