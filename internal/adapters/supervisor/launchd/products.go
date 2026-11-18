package launchd

import (
	"encoding/xml"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/morzecrew/morzer/internal/domain"
)

// BootJobProducts names the products whose boot job the manager wrote in dir
// for an installation under root, for a shell completion that cannot read the
// installations themselves: every user can read a plist in DaemonDir.
//
// Recognised by what the job runs, as Units writes it, not by its label alone:
// `apply --startup` with this product's installation file. So a schedule's job,
// another tool's job, or one for an installation under another root is not
// offered, and neither is a plist in a format other than the XML Units writes.
func BootJobProducts(dir, root string) []string {
	files, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var products []string
	for _, f := range files {
		label, ok := strings.CutSuffix(f.Name(), ".plist")
		product, isOurs := strings.CutPrefix(label, labelPrefix)
		if !ok || !isOurs || f.IsDir() || domain.ValidateProductName(product) != nil {
			continue
		}
		args, err := programArguments(filepath.Join(dir, f.Name()))
		if err != nil || len(args) < 6 || filepath.Base(args[0]) != "morzer" ||
			!slices.Equal(args[1:6], []string{"apply", "--startup", "--plain", "--log-format", "json"}) {
			continue
		}
		paths := domain.PathsUnder(root, product)
		switch rest := args[6:]; {
		case len(rest) == 0 && paths.Root() == "",
			len(rest) == 2 && rest[0] == "--config" && rest[1] == paths.InstallationFile():
			products = append(products, product)
		}
	}
	return products
}

// programArguments reads a plist's ProgramArguments.
func programArguments(path string) ([]string, error) {
	f, err := os.Open(path) //nolint:gosec // a plist in the directory named above
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	d := xml.NewDecoder(f)
	var key string
	for {
		tok, err := d.Token()
		if errors.Is(err, io.EOF) {
			return nil, errors.New("no ProgramArguments")
		}
		if err != nil {
			return nil, err
		}
		start, ok := tok.(xml.StartElement)
		switch {
		case !ok:
		case start.Name.Local == "key":
			if err := d.DecodeElement(&key, &start); err != nil {
				return nil, err
			}
		case start.Name.Local == "array" && key == "ProgramArguments":
			var array struct {
				Strings []string `xml:"string"`
			}
			if err := d.DecodeElement(&array, &start); err != nil {
				return nil, err
			}
			return array.Strings, nil
		}
	}
}
