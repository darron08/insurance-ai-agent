# 通用保险 AI Agent

当前完成 M1：通过 Go 命令解析本地文本型 PDF，输出带产品信息、页码和 SHA-256 的 JSON。

## 环境与运行

需要 Go 1.22 或更高版本、Python 3 和 `pdfplumber 0.11.9`；不需要 API Key。首次运行前安装锁定的 Python 依赖：

```bash
python3 -m pip install -r requirements.txt
```

在项目根目录执行：

```bash
go run ./cmd/ingest \
  --file ./testdata/sample.pdf \
  --product-id demo \
  --document-name "自建解析测试资料" \
  --output ./var/imports
```

`sample.pdf` 是项目自行生成的三页中英文测试资料，不是真实保险产品，不包含可用于保险解释的真实条款。来源 URL 是示例标记，不会被下载；导入始终读取 `--file` 指定的本地文件。

查看终端输出的 JSON 路径，即可检查元数据、物理页码和正文。`var/` 中的生成结果已加入 Git 忽略规则。

```bash
go test ./...
go vet ./...
go build -o ./bin/ingest ./cmd/ingest
./bin/ingest --help
```

## 参数

| 参数 | 含义 | 要求 |
| --- | --- | --- |
| `--file` | 本地 PDF 路径 | 必填，普通文件且具有 PDF 文件头 |
| `--product-id` | 产品标识 | 必填 |
| `--document-name` | 文档名称 | 可选；未传时使用 PDF 文件名并去掉 `.pdf` 后缀 |
| `--source-url` | 文档来源地址 | 可选；未传时记录为 `local://文件名`，填写时需为 HTTP/HTTPS URL；只记录，不请求 |
| `--output` | 输出目录 | 默认 `./var/imports` |

可以用实际保险 PDF 替换文件路径；有公开来源时再填写真实 URL。项目附带两份已下载的平安公开条款及来源说明，见 [`testdata/real/SOURCES.md`](testdata/real/SOURCES.md)。完成后请对照原文抽查至少三页。

成功返回退出码 0；参数语法错误返回 2；输入、解析或输出失败返回 1。`go run` 本身会包装程序退出码；需要直接检查原始退出码时，请使用构建后的 `./bin/ingest`。

## 输出与重复运行

每次成功导入产生一个独立 JSON 文件，包含：

- `metadata`：产品、名称、来源；`version` 字段当前留空，后续文档版本管理阶段再启用。
- `file_path`、`sha256`：绝对文件路径、文件内容的校验值。
- `total_pages`、`character_count`：总页数、提取并去掉页面首尾空白后的 Unicode 码点数。
- `pages`：`page_number` 从 1 开始，包括空白页；`text` 为页面文本。
- `quality`：质量门槛结果、替换字符数、清理的私用区字符数、文本页和空白页数量。
- `imported_at`、`parser`：UTC 导入时间、解析器及实际版本。

先写临时文件、关闭成功后才改名为 `.json`，避免将不完整输出作为成功结果。重复导入保留原输出，同一文件的校验值一致，输出文件名和导入时间可以不同。M3 再加入基于数据库的查重和幂等状态；M1 没有 `document_id`、自动跳过或 `--reindex`。

## 当前限制

正文由 `pdfplumber` 按 PDF 内容流恢复，从而避免双栏文字被逐行交错；复杂表格、旋转文字或本身缺少正确内容顺序的 PDF 仍需人工核验。页码是 PDF 的物理页面序号，可能不同于正文印刷的页码。

Go 负责校验、SHA-256、质量门槛和 JSON 落盘，内嵌 Python 脚本只调用 `pdfplumber` 逐页提取。出现 `U+FFFD` 替换字符或全文为空会拒绝导入，不会回退到旧解析器；空白页与非空页面混合时保留空白页。M1 不执行 OCR。`quality.status: passed` 表示自动门槛通过，不等同于对条款内容完成人工审校。

代码阅读、原理和动手实验见 [M1 学习说明](docs/learning/m1-pdf-ingest.md)。
