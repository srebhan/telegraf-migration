package main

import (
	"bufio"
	"bytes"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"sort"
	"strings"

	"github.com/influxdata/toml"
	"github.com/influxdata/toml/ast"

	"github.com/srebhan/test/migrations"
	_ "github.com/srebhan/test/migrations/all"
)

type section struct {
	name    string
	begin   int
	content *ast.Table
	raw     *bytes.Buffer
}

func splitToSections(root *ast.Table) []section {
	var sections []section
	for name, elements := range root.Fields {
		switch name {
		case "inputs", "outputs", "processors", "aggregators":
			category, ok := elements.(*ast.Table)
			if !ok {
				log.Fatalf("%q is not a table (%T)", name, category)
			}

			for plugin, elements := range category.Fields {
				tbls, ok := elements.([]*ast.Table)
				if !ok {
					log.Fatalf("elements of \"%s.%s\" is not a list of tables (%T)", name, plugin, elements)
				}
				for _, tbl := range tbls {
					s := section{
						name:    name + "." + tbl.Name,
						begin:   tbl.Line,
						content: tbl,
						raw:     &bytes.Buffer{},
					}
					sections = append(sections, s)
				}
			}
		default:
			tbl, ok := elements.(*ast.Table)
			if !ok {
				log.Fatalf("%q is not a table (%T)", name, elements)
			}
			s := section{
				name:    name,
				begin:   tbl.Line,
				content: tbl,
				raw:     &bytes.Buffer{},
			}
			sections = append(sections, s)
		}
	}

	// Sort the TOML elements by begin (line-number)
	sort.SliceStable(sections, func(i, j int) bool { return sections[i].begin < sections[j].begin })

	return sections
}

func assignTextToSections(data []byte, sections []section) {
	// Now assign the raw text to each section
	if sections[0].begin > 0 {
		sections = append([]section{{
			name:  "header",
			begin: 0,
			raw:   &bytes.Buffer{},
		}}, sections...)
	}

	var lineno int
	scanner := bufio.NewScanner(bytes.NewBuffer(data))
	for idx, next := range sections[1:] {
		var buf bytes.Buffer
		for lineno < next.begin-1 {
			if !scanner.Scan() {
				break
			}
			lineno++

			line := strings.TrimSpace(scanner.Text())
			if strings.HasPrefix(line, "#") {
				_, _ = buf.Write(scanner.Bytes())
				_, _ = buf.WriteString("\n")
				continue
			}

			if line == "" && buf.Len() > 0 {
				if _, err := io.Copy(sections[idx].raw, &buf); err != nil {
					log.Fatalf("copying buffer failed: %v", err)
				}
				buf.Reset()
			}

			_, _ = sections[idx].raw.Write(scanner.Bytes())
			_, _ = sections[idx].raw.WriteString("\n")
		}
		if err := scanner.Err(); err != nil {
			log.Fatalf("splitting by line failed: %v", err)
		}

		// If a comment is directly in front of the next section, without
		// newline, the comment is assigned to the next section.
		if buf.Len() > 0 {
			if _, err := io.Copy(sections[idx+1].raw, &buf); err != nil {
				log.Fatalf("copying buffer failed: %v", err)
			}
			buf.Reset()
		}
	}
	// Write the remaining to the last section
	for scanner.Scan() {
		_, _ = sections[len(sections)-1].raw.Write(scanner.Bytes())
		_, _ = sections[len(sections)-1].raw.WriteString("\n")
	}
	if err := scanner.Err(); err != nil {
		log.Fatalf("splitting by line failed: %v", err)
	}
}

func Usage() {
	fmt.Fprintf(flag.CommandLine.Output(), "Usage: %s [options] <config file(s)>\n", os.Args[0])
	fmt.Fprint(flag.CommandLine.Output(), `
  Migrates deprecated plugin in Telegraf configuration files to the new
  recommended setup.

  NOTE: There is NO GUARANTEE that the generate configuration is equivalent
        to the old configuration, Please check the resulting configuration
        and metrics!

`)
	fmt.Fprintln(flag.CommandLine.Output(), "Options:")
	flag.PrintDefaults()
}

func main() {
	flag.Usage = Usage

	// Define options
	var debug, help bool
	flag.BoolVar(&debug, "debug", false, "print debugging information")
	flag.BoolVar(&help, "help", false, "print this help text")
	flag.Parse()

	if help {
		flag.Usage()
		os.Exit(0)
	}

	if flag.NArg() == 0 {
		flag.Usage()
		os.Exit(1)
	}

	for _, filename := range flag.Args() {
		// Read and parse the config file
		data, err := os.ReadFile(filename)
		if err != nil {
			log.Fatalf("Opening %q failed: %v", filename, err)
		}

		root, err := toml.Parse(data)
		if err != nil {
			log.Fatalf("Parsing %q failed: %v", filename, err)
		}

		// Split the configuration into sections containing the location
		// in the file.
		sections := splitToSections(root)
		if len(sections) == 0 {
			log.Fatalln("no TOML configuration found")
		}

		// Assign the configuration text to the corresponding segments
		assignTextToSections(data, sections)

		// Do the actual migration(s)
		for idx, s := range sections {
			migrate, found := migrations.PluginMigrations[s.name]
			if !found {
				continue
			}

			log.Printf("Migrating plugin %q in line %d...", s.name, s.begin)
			result, err := migrate(s.content)
			if err != nil {
				log.Fatalf("migrating %q (line %d) failed: %v", s.name, s.begin, err)
			}
			s.raw = bytes.NewBuffer(result)
			sections[idx] = s

			if debug {
				fmt.Println("=================================================")
				fmt.Println(s.name)
				fmt.Println("-------------------------------------------------")
				fmt.Println(s.raw.String())
				fmt.Println("-------------------------------------------------")
				for k, content := range s.content.Fields {
					fmt.Printf("%s: %v (%T)\n", k, content, content)
					switch v := content.(type) {
					case *ast.KeyValue:
						fmt.Printf("  -> %s: %v (%T)\n", v.Key, v.Value, v.Value)
					}
				}
				fmt.Println("=================================================")
			}
		}

		// Write the output file
		outfn := filename + ".migrated"
		file, err := os.OpenFile(outfn, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0644)
		if err != nil {
			log.Fatalf("Opening %q failed: %v", outfn, err)
		}
		defer file.Close()

		for _, s := range sections {
			_, err = s.raw.WriteTo(file)
			if err != nil {
				log.Fatalf("Writing to %q failed: %v", outfn, err)
			}
		}
	}
}
