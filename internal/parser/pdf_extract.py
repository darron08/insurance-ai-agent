import json
import re
import sys
import unicodedata

try:
    import pdfplumber
except ModuleNotFoundError:
    print(
        "缺少 Python 依赖 pdfplumber，请执行: python3 -m pip install -r requirements.txt",
        file=sys.stderr,
    )
    raise SystemExit(3)


CJK_SPACED_RUN = re.compile(
    r"(?:[\u3400-\u4dbf\u4e00-\u9fff][ \u00a0]+){2,}"
    r"[\u3400-\u4dbf\u4e00-\u9fff]"
)


def clean_text(value):
    value = unicodedata.normalize("NFC", value or "")
    private_use_count = 0
    cleaned = []
    for character in value:
        if unicodedata.category(character) == "Co":
            private_use_count += 1
            cleaned.append(" ")
        else:
            cleaned.append(character)
    value = "".join(cleaned)
    value = CJK_SPACED_RUN.sub(
        lambda match: re.sub(r"[ \u00a0]+", "", match.group(0)), value
    )
    lines = [line.rstrip() for line in value.splitlines()]
    value = "\n".join(lines).strip()
    return value, private_use_count


def main():
    if len(sys.argv) != 2:
        print("用法: pdf_extract.py <pdf-path>", file=sys.stderr)
        return 2

    pages = []
    private_use_characters_removed = 0
    try:
        with pdfplumber.open(sys.argv[1]) as document:
            for number, page in enumerate(document.pages, start=1):
                # Content-stream order keeps columns separate instead of
                # interleaving their lines by equal Y coordinates.
                text, removed = clean_text(
                    page.extract_text(layout=False, use_text_flow=True) or ""
                )
                private_use_characters_removed += removed
                pages.append({"page_number": number, "text": text})
    except Exception as error:
        print(f"无法解析 PDF: {error}", file=sys.stderr)
        return 1

    json.dump(
        {
            "engine": f"pdfplumber@{pdfplumber.__version__}",
            "private_use_characters_removed": private_use_characters_removed,
            "pages": pages,
        },
        sys.stdout,
        ensure_ascii=False,
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
