package officecapture

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"time"

	"github.com/ourostack/m365crawl/internal/officedocuments"
)

var (
	discoveryHome = os.UserHomeDir
	discoveryRead = (*os.File).Readdirnames
	discoveryMac  = runtime.GOOS == "darwin"
)

func inputNames(ctx context.Context, path string) ([]string, error) {
	if ctx.Err() != nil {
		return nil, jsonError(ctx, "office_json_input_unqualified")
	}
	input, err := openInput(path, true)
	if err != nil {
		return nil, err
	}
	names, readErr := discoveryRead(input.file(), 129)
	closeErr := input.close()
	if len(names) > 128 || (readErr != nil && !errors.Is(readErr, io.EOF)) ||
		closeErr != nil || ctx.Err() != nil {
		return nil, jsonError(ctx, "office_json_input_unqualified")
	}
	slices.Sort(names)
	return names, nil
}

func DiscoverJSON(ctx context.Context, root string) (inputs []DocumentInput, result error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	result = withSourceLifetime(ctx, "office_json_input_unqualified", func() error {
		return withInputGuard(ctx, func() error {
			if root != "" {
				names, err := inputNames(ctx, root)
				if err != nil {
					return jsonError(ctx, "office_json_input_unqualified")
				}
				for _, surface := range []officedocuments.Surface{officedocuments.Recent, officedocuments.Shared, officedocuments.Recommended, officedocuments.Dialog} {
					name := string(surface) + ".json"
					if slices.Contains(names, name) {
						inputs = append(inputs, DocumentInput{Path: filepath.Join(root, name), Surface: surface, Application: "supplied"})
					}
				}
			} else {
				if !discoveryMac {
					return jsonError(ctx, "office_json_input_unqualified")
				}
				home, err := discoveryHome()
				if err != nil {
					return jsonError(ctx, "office_json_input_unqualified")
				}
				for _, app := range []struct{ container, application, prefix string }{
					{"Word", "Word", "w"}, {"Excel", "Excel", "x"}, {"Powerpoint", "PowerPoint", "p"},
				} {
					office := filepath.Join(home, "Library", "Containers", "com.microsoft."+app.container,
						"Data", "Library", "Application Support", "Microsoft", "Office", "16.0")
					for _, group := range []string{"aggmru", "MruServiceCache"} {
						base := filepath.Join(office, group)
						accounts, err := inputNames(ctx, base)
						if errors.Is(err, os.ErrNotExist) {
							continue
						}
						if err != nil {
							return jsonError(ctx, "office_json_input_unqualified")
						}
						for _, account := range accounts {
							directory := filepath.Join(base, account)
							names, err := inputNames(ctx, directory)
							if err != nil {
								return jsonError(ctx, "office_json_input_unqualified")
							}
							if group == "aggmru" {
								for _, surface := range []struct {
									surface officedocuments.Surface
									suffix  string
								}{{officedocuments.Recent, "mru4"}, {officedocuments.Shared, "swm"}, {officedocuments.Recommended, "rec"}} {
									name := app.prefix + "-" + surface.suffix + "-en-US-sr.json"
									if slices.Contains(names, name) {
										inputs = append(inputs, DocumentInput{Path: filepath.Join(directory, name), Surface: surface.surface, Application: app.application})
									}
								}
							} else {
								for _, name := range names {
									leaves, err := inputNames(ctx, filepath.Join(directory, name))
									if err != nil {
										return jsonError(ctx, "office_json_input_unqualified")
									}
									if slices.Contains(leaves, "Documents_en-US") {
										inputs = append(inputs, DocumentInput{Path: filepath.Join(directory, name, "Documents_en-US"), Surface: officedocuments.Dialog, Application: app.application})
									}
								}
							}
							if len(inputs) > 128 {
								return jsonError(ctx, "office_json_input_unqualified")
							}
						}
					}
				}
			}
			if len(inputs) == 0 {
				return jsonError(ctx, "office_json_input_unqualified")
			}
			return nil
		})
	})
	if result != nil {
		inputs = nil
	}
	return inputs, result
}
