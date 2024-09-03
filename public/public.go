package public

import (
	_ "embed"
)

var (
	//go:embed backdrop.webp
	BackdropWebP []byte

	//go:embed favicon.svg
	FaviconSVG []byte

	//go:embed index.css
	IndexCSS []byte

	//go:embed index.html
	IndexHTML []byte

	//go:embed index.js
	IndexJS []byte
)
