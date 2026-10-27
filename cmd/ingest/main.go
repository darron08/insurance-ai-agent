package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"insurance_agent_demo/internal/ingest"
	"insurance_agent_demo/internal/parser"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("ingest", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var req ingest.Request
	flags.StringVar(&req.File, "file", "", "本地文本型 PDF 路径（必填）")
	flags.StringVar(&req.Metadata.ProductID, "product-id", "", "产品标识（必填）")
	flags.StringVar(&req.Metadata.Version, "version", "", "文档版本（可选，默认空版本）")
	flags.StringVar(&req.Metadata.Name, "document-name", "", "文档名称（可选，默认使用 PDF 文件名）")
	flags.StringVar(&req.Metadata.SourceURL, "source-url", "", "资料来源 HTTP/HTTPS URL（可选，默认本地来源）")
	flags.StringVar(&req.OutputDir, "output", "./var/imports", "JSON 输出目录")
	if err := flags.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if flags.NArg() > 0 {
		fmt.Fprintln(stderr, "不接受位置参数，请使用 --file 指定文件")
		return 2
	}
	doc, path, err := (ingest.Service{Parser: parser.PDF{}}).Run(req)
	if err != nil {
		fmt.Fprintln(stderr, "导入失败:", err)
		return 1
	}
	fmt.Fprintf(stdout, "导入成功\n文件: %s\n页数: %d\n字符数: %d\nSHA-256: %s\n输出: %s\n", doc.FilePath, doc.TotalPages, doc.CharacterCount, doc.SHA256, path)
	return 0
}
