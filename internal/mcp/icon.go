package mcpserver

import (
	_ "embed"
	"encoding/base64"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// receiptSVG is the Lucide "receipt" icon (ISC license, lucide.dev).
//
//go:embed receipt.svg
var receiptSVG string

// icons renders the icon once per theme: clients show it as an image, where
// currentColor does not follow the surrounding text color.
var icons = []mcp.Icon{receiptIcon("#18181b", mcp.IconThemeLight), receiptIcon("#fafafa", mcp.IconThemeDark)}

func receiptIcon(color string, theme mcp.IconTheme) mcp.Icon {
	svg := strings.Replace(strings.TrimSpace(receiptSVG), `stroke="currentColor"`, `stroke="`+color+`"`, 1)
	return mcp.Icon{Source: "data:image/svg+xml;base64," + base64.StdEncoding.EncodeToString([]byte(svg)), MIMEType: "image/svg+xml", Sizes: []string{"any"}, Theme: theme}
}
