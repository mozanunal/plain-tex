package app

import (
	"embed"
	"fmt"
	"html/template"
)

//go:embed templates/*.html
var templatesFS embed.FS

//go:embed static/*
var staticFS embed.FS

var templateFuncs = template.FuncMap{
	"dict": func(values ...any) (map[string]any, error) {
		if len(values)%2 != 0 {
			return nil, fmt.Errorf("dict requires an even number of arguments")
		}
		result := make(map[string]any, len(values)/2)
		for i := 0; i < len(values); i += 2 {
			key, ok := values[i].(string)
			if !ok {
				return nil, fmt.Errorf("dict keys must be strings")
			}
			result[key] = values[i+1]
		}
		return result, nil
	},
	"sub": func(a, b int) int { return a - b },
}

func loadTemplates() (*template.Template, error) {
	return template.New("app").Funcs(templateFuncs).ParseFS(templatesFS, "templates/*.html")
}
