package mesh

import (
	"fmt"
	"os"

	"github.com/BurntSushi/toml"
	"github.com/flipfloptech/cortex-mcp/internal/config"
	"github.com/spf13/cobra"
)

// buildImportExaCmd returns a command that parses Exascaler TOML and generates mesh.toml.
func buildImportExaCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "import-exa <path_to_exascaler_toml> [output_mesh.toml]",
		Short: "Convert an Exascaler TOML config to a cortex-mcp mesh.toml",
		Args:  cobra.RangeArgs(1, 2),
		Run: func(cmd *cobra.Command, args []string) {
			inputPath := args[0]
			outputPath := ""
			if len(args) > 1 {
				outputPath = args[1]
			}

			if err := convertExascalerToml(inputPath, outputPath); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
		},
	}
}

// exaGlobal represents the [global] section for mgt list parsing.
type exaGlobal struct {
	Mgt struct {
		List []string `toml:"list"`
	} `toml:"mgt"`
}

// exaFS represents the [fs] section for mdt and ost parsing.
type exaFS struct {
	MDT struct {
		List []string `toml:"list"`
	} `toml:"mdt"`
	OST struct {
		List []string `toml:"list"`
	} `toml:"ost"`
}

// convertExascalerToml reads the Exascaler definition and outputs a valid mesh.toml config.
func convertExascalerToml(inPath, outPath string) error {
	var raw map[string]interface{}
	if _, err := toml.DecodeFile(inPath, &raw); err != nil {
		return fmt.Errorf("decode exascaler toml: %w", err)
	}

	cfg := &config.MeshConfig{
		Hosts:  make(map[string]config.Host),
		Groups: make(map[string][]string),
	}

	// 1. Build Groups from global.mgt and fs.*.mdt / fs.*.ost
	var structured struct {
		Global exaGlobal        `toml:"global"`
		FS     map[string]exaFS `toml:"fs"`
	}
	if _, err := toml.DecodeFile(inPath, &structured); err != nil {
		return fmt.Errorf("decode exascaler toml structured: %w", err)
	}

	if len(structured.Global.Mgt.List) > 0 {
		cfg.Groups["mgt"] = structured.Global.Mgt.List
	}

	for fsName, fs := range structured.FS {
		if len(fs.MDT.List) > 0 {
			cfg.Groups[fsName+".mdt"] = fs.MDT.List
			cfg.Groups["mdt"] = append(cfg.Groups["mdt"], fs.MDT.List...)
		}
		if len(fs.OST.List) > 0 {
			cfg.Groups[fsName+".ost"] = fs.OST.List
			cfg.Groups["ost"] = append(cfg.Groups["ost"], fs.OST.List...)
		}
	}

	// 2. Parse Hosts to get IPs and SFA mappings
	if hostsRaw, ok := raw["host"].(map[string]interface{}); ok {
		for hostName, hostVal := range hostsRaw {
			hostMap, ok := hostVal.(map[string]interface{})
			if !ok {
				continue
			}

			// Extract SFA mapping and add to group e.g. "sfa.memp-aqq-38"
			if sfaNameRaw, ok := hostMap["sfa"]; ok {
				if sfaName, ok := sfaNameRaw.(string); ok && sfaName != "" {
					groupName := "sfa." + sfaName
					cfg.Groups[groupName] = append(cfg.Groups[groupName], hostName)
				}
			}

			// Extract all IPs
			var ips []string
			if nicMap, ok := hostMap["nic"].(map[string]interface{}); ok {
				for _, nicVal := range nicMap {
					nicCfg, ok := nicVal.(map[string]interface{})
					if !ok {
						continue
					}
					if ip, ok := nicCfg["ip"].(string); ok && ip != "" {
						ips = append(ips, ip)
					}
				}
			}

			if len(ips) > 0 {
				cfg.Hosts[hostName] = config.Host{Addresses: ips}
			}
		}
	}

	// 3. Output
	if outPath != "" {
		f, err := os.Create(outPath)
		if err != nil {
			return fmt.Errorf("create output file: %w", err)
		}
		defer func() {
			_ = f.Close()
		}()
		if err := toml.NewEncoder(f).Encode(cfg); err != nil {
			return fmt.Errorf("encode mesh config: %w", err)
		}
		fmt.Printf("Successfully wrote mesh configuration to %s\n", outPath)
	} else {
		if err := toml.NewEncoder(os.Stdout).Encode(cfg); err != nil {
			return fmt.Errorf("encode mesh config: %w", err)
		}
	}

	return nil
}
