# 真实条款测试样本

这些文件仅用于本项目的 PDF 解析与检索测试，不构成保险建议，也不替代保险公司的最新条款。

| 本地文件 | 产品/资料名称 | 官方来源 | 下载日期 |
| --- | --- | --- | --- |
| `pingan-ankang-te-xu-ban-2020-medical.pdf` | 平安安康（特需版 2020）医疗保险条款 | [中国平安健康险 PDF](https://resources.pingan.com/app_upload/file/health/aktxb20200751.pdf) | 2026-10-25 |
| `pingan-travel-accident-terms-handbook.pdf` | 平安境外旅行意外伤害保险等条款手册 | [中国平安财产险 PDF](https://www.pingan.com/app_upload/file/insurance/201004271656.pdf) | 2026-10-25 |

第二份资料含多份历史旅行险条款，排版较复杂；它主要用来验证复杂中文 PDF 的解析降级表现，而不是作为某个当前在售产品的准确说明。

导入示例：

```bash
go run ./cmd/ingest \
  --file ./testdata/real/pingan-ankang-te-xu-ban-2020-medical.pdf \
  --product-id pingan-ankang-2020 \
  --source-url https://resources.pingan.com/app_upload/file/health/aktxb20200751.pdf
```
