// Package testpdf builds synthetic PDF fixtures; it is not part of the import pipeline.
package testpdf

import (
	"bytes"
	"fmt"
	"strings"
)

// Build creates a small PDF with a Unicode CMap and optional blank pages.
// Page strings are ASCII; each nonblank page also includes Chinese fixture text.
func Build(pages ...string) []byte {
	const chinese = "保险条款测试资料"
	var cmap, codes strings.Builder
	fmt.Fprintf(&cmap, "/CIDInit /ProcSet findresource begin\n12 dict begin\nbegincmap\n/CIDSystemInfo << /Registry (Adobe) /Ordering (UCS) /Supplement 0 >> def\n/CMapName /Test def\n/CMapType 2 def\n1 begincodespacerange\n<0000> <FFFF>\nendcodespacerange\n%d beginbfchar\n", len([]rune(chinese)))
	for i, r := range []rune(chinese) {
		fmt.Fprintf(&cmap, "<%04X> <%04X>\n", i+1, r)
		fmt.Fprintf(&codes, "%04X", i+1)
	}
	cmap.WriteString("endbfchar\nendcmap\nCMapName currentdict /CMap defineresource pop\nend\nend\n")
	var kids strings.Builder
	for i := range pages {
		fmt.Fprintf(&kids, "%d 0 R ", 7+2*i)
	}
	stream := func(s string) string { return fmt.Sprintf("<< /Length %d >>\nstream\n%sendstream", len(s), s) }
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		fmt.Sprintf("<< /Type /Pages /Count %d /Kids [%s] >>", len(pages), kids.String()),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding >>",
		"<< /Type /Font /Subtype /Type0 /BaseFont /STSong-Light /Encoding /Identity-H /DescendantFonts [5 0 R] /ToUnicode 6 0 R >>",
		"<< /Type /Font /Subtype /CIDFontType0 /BaseFont /STSong-Light /CIDSystemInfo << /Registry (Adobe) /Ordering (GB1) /Supplement 0 >> /DW 1000 >>",
		stream(cmap.String()),
	}
	for i, text := range pages {
		objects = append(objects, fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 3 0 R /F2 4 0 R >> >> /Contents %d 0 R >>", 8+2*i))
		content := ""
		if text != "" {
			escaped := strings.NewReplacer("\\", "\\\\", "(", "\\(", ")", "\\)").Replace(text)
			content = fmt.Sprintf("BT /F1 12 Tf 72 720 Td (%s) Tj ET\nBT /F2 12 Tf 72 690 Td <%s> Tj ET\n", escaped, codes.String())
		}
		objects = append(objects, stream(content))
	}
	var out bytes.Buffer
	out.WriteString("%PDF-1.4\n")
	offsets := []int{0}
	for i, obj := range objects {
		offsets = append(offsets, out.Len())
		fmt.Fprintf(&out, "%d 0 obj\n%s\nendobj\n", i+1, obj)
	}
	xref := out.Len()
	fmt.Fprintf(&out, "xref\n0 %d\n0000000000 65535 f \n", len(offsets))
	for _, offset := range offsets[1:] {
		fmt.Fprintf(&out, "%010d 00000 n \n", offset)
	}
	fmt.Fprintf(&out, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offsets), xref)
	return out.Bytes()
}
