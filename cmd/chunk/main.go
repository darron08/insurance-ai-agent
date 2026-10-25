package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"insurance_agent_demo/internal/chunker"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("chunk", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var input, output string
	flags.StringVar(&input, "input", "", "M1 导入 JSON 路径（必填）")
	flags.StringVar(&output, "output", "./var/chunks", "Chunk JSON 输出目录")
	if err := flags.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if flags.NArg() > 0 {
		fmt.Fprintln(stderr, "不接受位置参数，请使用 --input 指定 M1 JSON")
		return 2
	}
	result, path, err := (chunker.Service{Splitter: chunker.ClauseAware{}}).Run(input, output)
	if err != nil {
		fmt.Fprintln(stderr, "切分失败:", err)
		return 1
	}
	fmt.Fprintf(stdout, "切分成功\n输入: %s\n页数: %d\nChunk 数量: %d\n策略: %s\n输出: %s\n", input, result.TotalPages, result.TotalChunks, result.Chunking.Strategy, path)
	return 0
}
