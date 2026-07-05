package ocr

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"

	"ocr-router/internal/config"
)

// PDFRenderer rasterizes PDF files into per-page image files.
type PDFRenderer struct {
	cfg config.PDFConfig
}

// NewPDFRenderer creates a renderer bound to the given config.
func NewPDFRenderer(cfg config.PDFConfig) *PDFRenderer {
	return &PDFRenderer{cfg: cfg}
}

// IsPDF reports whether the given path looks like a PDF file (by extension).
func IsPDF(path string) bool {
	return strings.ToLower(filepath.Ext(path)) == ".pdf"
}

// PageImage represents a single rasterized page.
type PageImage struct {
	Path  string // absolute path to the rendered image file
	Page  int    // 1-based page number
	MIME  string // image MIME type
	Bytes int64  // file size in bytes
}

// resolveBinary finds the absolute path of a tool in this priority order:
//  1. explicit config override (BinPath / InfoBinPath),
//  2. a binary shipped in the project's ./bin directory,
//  3. any directory on PATH.
//
// notFoundMsg formats a helpful, install-oriented message when nothing is found.
func (r *PDFRenderer) resolveBinary(name, override string) (string, error) {
	if override != "" {
		if _, err := os.Stat(override); err == nil {
			return override, nil
		}
		if p, err := exec.LookPath(override); err == nil {
			return p, nil
		}
	}
	if p := inProjectBin(name); p != "" {
		return p, nil
	}
	if p, err := exec.LookPath(name); err == nil {
		return p, nil
	}
	return "", fmt.Errorf("%s", missingToolMsg(name))
}

// CountPages returns the number of pages in a PDF.
// It prefers pdfinfo; on failure it falls back to 0 which callers may
// interpret as "render whatever exists".
func (r *PDFRenderer) CountPages(pdfPath string) (int, error) {
	bin, err := r.resolveBinary("pdfinfo", r.cfg.InfoBinPath)
	if err != nil {
		// pdfinfo is optional; absence is not a hard error.
		return 0, nil
	}

	cmd := exec.Command(bin, pdfPath)
	out, err := cmd.Output()
	if err != nil {
		return 0, nil
	}
	re := regexp.MustCompile(`(?m)^Pages:\s+(\d+)\s*$`)
	m := re.FindSubmatch(out)
	if len(m) < 2 {
		return 0, nil
	}
	n, err := strconv.Atoi(string(m[1]))
	if err != nil || n < 0 {
		return 0, nil
	}
	return n, nil
}

// Render converts a PDF into one image per page and returns the page list.
// The caller is responsible for cleaning up the returned page files via
// Cleanup(). The temp directory used is also tracked for removal.
func (r *PDFRenderer) Render(pdfPath string) ([]PageImage, string, error) {
	if _, err := os.Stat(pdfPath); err != nil {
		return nil, "", fmt.Errorf("pdf not found: %w", err)
	}

	tool, bin, err := r.resolveTool()
	if err != nil {
		return nil, "", err
	}

	tmpDir, err := os.MkdirTemp(r.cfg.TempDir, "ocr-pdf-")
	if err != nil {
		return nil, "", fmt.Errorf("failed to create temp dir: %w", err)
	}

	if err := r.renderWith(tool, bin, pdfPath, tmpDir); err != nil {
		os.RemoveAll(tmpDir)
		return nil, "", err
	}

	pages, err := r.collectPages(tmpDir)
	if err != nil {
		os.RemoveAll(tmpDir)
		return nil, "", err
	}

	if r.cfg.MaxPages > 0 && len(pages) > r.cfg.MaxPages {
		// Remove pages beyond the limit
		for _, p := range pages[r.cfg.MaxPages:] {
			os.Remove(p.Path)
		}
		pages = pages[:r.cfg.MaxPages]
	}

	return pages, tmpDir, nil
}

// resolveTool picks the renderer to use, returning the tool name and the
// absolute path of its binary. Discovery order:
//  1. explicit config (cfg.Tool + cfg.BinPath), validated to exist;
//  2. ./bin/<tool> shipped with the project;
//  3. PATH lookup; first of pdftoppm, then mutool.
//
// When cfg.Tool is set but that specific tool cannot be found, the function
// returns an error rather than silently falling back to another tool, so the
// user's explicit choice is respected.
func (r *PDFRenderer) resolveTool() (name string, bin string, err error) {
	// Explicit configured tool: must exist, do not fall back.
	if r.cfg.Tool != "" {
		p, e := r.resolveBinary(r.cfg.Tool, r.cfg.BinPath)
		if e != nil {
			return "", "", fmt.Errorf("configured pdf.tool %q not found: %v", r.cfg.Tool, e)
		}
		return r.cfg.Tool, p, nil
	}

	// Auto-detect: prefer pdftoppm, then mutool.
	for _, c := range []string{"pdftoppm", "mutool"} {
		if p, e := r.resolveBinary(c, ""); e == nil {
			return c, p, nil
		}
	}

	return "", "", fmt.Errorf("no PDF renderer available. %s",
		missingToolMsg(""))
}

// renderWith invokes the chosen renderer to produce images in tmpDir.
func (r *PDFRenderer) renderWith(name, bin, pdfPath, tmpDir string) error {
	dpi := r.cfg.DPI
	if dpi <= 0 {
		dpi = 200
	}
	prefix := filepath.Join(tmpDir, "page")
	outPrefix := prefix + "-"

	switch name {
	case "pdftoppm":
		args := []string{
			"-r", strconv.Itoa(dpi),
			"-png",
		}
		if r.cfg.MaxPages > 0 {
			args = append(args, "-l", strconv.Itoa(r.cfg.MaxPages))
		}
		args = append(args, pdfPath, prefix)
		cmd := exec.Command(bin, args...)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("pdftoppm failed: %w: %s", err, strings.TrimSpace(string(out)))
		}
		return nil

	case "mutool":
		// mutool draw -o prefix-%d.png -r <dpi> input.pdf
		format := r.cfg.Format
		if format == "" {
			format = "png"
		}
		pattern := outPrefix + "%d." + format
		args := []string{"draw", "-o", pattern, "-r", strconv.Itoa(dpi)}
		if r.cfg.MaxPages > 0 {
			args = append(args, "1-"+strconv.Itoa(r.cfg.MaxPages))
		}
		args = append(args, pdfPath)
		cmd := exec.Command(bin, args...)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("mutool failed: %w: %s", err, strings.TrimSpace(string(out)))
		}
		return nil

	default:
		return fmt.Errorf("unsupported pdf tool: %s", name)
	}
}

// collectPages scans tmpDir for rendered page images and returns them ordered.
func (r *PDFRenderer) collectPages(tmpDir string) ([]PageImage, error) {
	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		return nil, err
	}

	var pages []PageImage
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		ext := strings.ToLower(filepath.Ext(name))
		if ext == ".pdf" {
			continue
		}
		mime := mimeForExt(ext)
		if mime == "" {
			continue
		}
		page, ok := pageFromName(name)
		if !ok {
			continue
		}
		full := filepath.Join(tmpDir, name)
		info, err := os.Stat(full)
		if err != nil {
			continue
		}
		pages = append(pages, PageImage{
			Path:  full,
			Page:  page,
			MIME:  mime,
			Bytes: info.Size(),
		})
	}

	if len(pages) == 0 {
		return nil, fmt.Errorf("pdf rendered no pages (empty or corrupted pdf?)")
	}

	// Sort by page number (stable enough given the small N).
	for i := 1; i < len(pages); i++ {
		for j := i; j > 0 && pages[j].Page < pages[j-1].Page; j-- {
			pages[j], pages[j-1] = pages[j-1], pages[j]
		}
	}
	return pages, nil
}

// pageFromName extracts the 1-based page number from rendered filenames.
// pdftoppm: page-1.png, page-01.png, page-001.png ...
// mutool:   page-1.png
func pageFromName(name string) (int, bool) {
	base := strings.TrimSuffix(name, filepath.Ext(name))
	idx := strings.LastIndexByte(base, '-')
	if idx < 0 {
		return 0, false
	}
	n, err := strconv.Atoi(base[idx+1:])
	if err != nil || n < 1 {
		return 0, false
	}
	return n, true
}

func mimeForExt(ext string) string {
	switch ext {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".webp":
		return "image/webp"
	case ".bmp":
		return "image/bmp"
	default:
		return ""
	}
}

// Cleanup removes a temp directory created by Render, ignoring errors.
func Cleanup(dir string) {
	if dir != "" {
		os.RemoveAll(dir)
	}
}

// inProjectBin checks for a tool shipped inside the project's ./bin directory
// (with an OS-appropriate ".exe" suffix on Windows) and returns its absolute
// path when present. The lookup walks up from the working directory until it
// finds a "bin" sibling of "go.mod", so it also works from subdirectories.
func inProjectBin(name string) string {
	if runtime.GOOS == "windows" {
		name += ".exe"
	}

	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	dir := wd
	for i := 0; i < 10; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			candidate := filepath.Join(dir, "bin", name)
			if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
				return candidate
			}
			return ""
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return ""
}

// missingToolMsg formats a user-actionable hint explaining how to obtain the
// PDF renderer. Pass an empty tool name for the generic "any tool" case.
func missingToolMsg(tool string) string {
	binDir := filepath.Join(projectRoot(), "bin")
	var specific string
	switch tool {
	case "pdftoppm", "pdfinfo":
		specific = fmt.Sprintf("install poppler-utils:\n  %s\n", popplerInstallHint())
	case "mutool":
		specific = fmt.Sprintf("install mupdf-tools:\n  %s\n", mupdfInstallHint())
	default:
		specific = fmt.Sprintf("install one of:\n  %s\n  %s\n",
			popplerInstallHint(), mupdfInstallHint())
	}
	return strings.TrimRight(specific, "\n") + fmt.Sprintf(
		"\nor place the `pdftoppm` / `mutool` binary in the project bin/ folder:\n  %s\n",
		binDir,
	)
}

func popplerInstallHint() string {
	switch runtime.GOOS {
	case "darwin":
		return "brew install poppler"
	case "windows":
		return "choco install poppler   (or download poppler and add it to PATH)"
	default:
		return "sudo apt-get install -y poppler-utils   # or: dnf install poppler-utils / pacman -S poppler"
	}
}

func mupdfInstallHint() string {
	switch runtime.GOOS {
	case "darwin":
		return "brew install mupdf-tools"
	case "windows":
		return "download mutool from mupdf.com and add it to PATH"
	default:
		return "sudo apt-get install -y mupdf-tools   # or: dnf install mupdf / pacman -S mupdf-tools"
	}
}

// projectRoot best-effort locates the dir containing go.mod (used to point the
// user at ./bin). Returns "" if not found.
func projectRoot() string {
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	dir := wd
	for i := 0; i < 10; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return wd
		}
		dir = parent
	}
	return wd
}
