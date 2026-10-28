package portal

import (
	"bytes"
	_ "embed"
	"errors"
	"html/template"
	"strings"

	"github.com/thaynes43/dev-env/internal/pages"
)

//go:embed pages.html
var pagesSource string

const pagesStyle = `
.pages-content{flex:1;min-height:0;overflow:auto;padding:24px;overflow-wrap:anywhere}
.pages-content .panel{max-width:72rem;margin:0 auto 24px}
.pages-content .turn-text{margin:10px 0}
@media(max-width:700px){.pages-content{overflow:visible;padding:18px 16px}.pages-content .panel{margin-bottom:24px}}
`

var PagesPolicy = policy(viewsStyle + pagesStyle)
var pagesTemplate = template.Must(template.New("pages").Parse(strings.Replace(pagesSource, "/*PORTAL_STYLE*/", viewsStyle+pagesStyle, 1)))

type boundedPagesBuffer struct {
	bytes.Buffer
	limit int
}

func (b *boundedPagesBuffer) Write(p []byte) (int, error) {
	if len(p) > b.limit-b.Len() {
		return 0, errors.New("pages render unavailable")
	}
	return b.Buffer.Write(p)
}

// RenderPages returns the entire escaped view or an error, never a partial page.
func RenderPages(snapshot pages.AlertmanagerSnapshot, limit int) ([]byte, error) {
	if limit <= 0 {
		return nil, errors.New("pages render unavailable")
	}
	b := &boundedPagesBuffer{limit: limit}
	err := pagesTemplate.Execute(b, struct {
		Snapshot       pages.AlertmanagerSnapshot
		Banner, Policy string
	}{snapshot, pages.AlertmanagerBanner, strings.TrimSuffix(PagesPolicy, "; frame-ancestors 'none'")})
	if err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}
