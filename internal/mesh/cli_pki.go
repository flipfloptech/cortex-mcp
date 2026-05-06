package mesh

import (
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"
)

// newPKICmd creates the root `cortex-mcp pki` command.
func newPKICmd() *cobra.Command {
	pkiCmd := &cobra.Command{
		Use:   "pki",
		Short: "Manage the Mesh CA and issue node certificates",
	}

	var forceFlag bool
	generateCmd := &cobra.Command{
		Use:   "generate",
		Short: "Generate a new Mesh CA",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPKIGenerate(cmd.OutOrStdout(), forceFlag)
		},
	}
	generateCmd.Flags().BoolVar(&forceFlag, "force", false, "Overwrite existing Mesh CA")

	var outFile string
	issueCmd := &cobra.Command{
		Use:   "issue <node_id>",
		Short: "Issue a node certificate signed by the Mesh CA",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPKIIssue(cmd.OutOrStdout(), args[0], outFile)
		},
	}
	issueCmd.Flags().StringVarP(&outFile, "out", "o", "", "Output file (default: stdout)")

	showCmd := &cobra.Command{
		Use:   "show",
		Short: "Show details about the local Mesh CA",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPKIShow(cmd.OutOrStdout())
		},
	}

	pkiCmd.AddCommand(generateCmd, issueCmd, showCmd)
	return pkiCmd
}

func runPKIGenerate(out io.Writer, force bool) error {
	path, err := caPath()
	if err != nil {
		return fmt.Errorf("failed to determine CA path: %w", err)
	}

	if !force {
		if _, err := os.Stat(path); err == nil {
			return fmt.Errorf("mesh CA already exists at %s, use --force to overwrite", path)
		}

		legacy, lErr := legacyCAPath()
		if lErr == nil {
			if _, err := os.Stat(legacy); err == nil {
				return fmt.Errorf("legacy Gateway CA exists at %s, use --force to overwrite and migrate", legacy)
			}
		}
	}

	// Remove existing to force loadOrGeneratePKI to generate
	_ = os.Remove(path)
	legacy, _ := legacyCAPath()
	if legacy != "" {
		_ = os.Remove(legacy)
	}

	_, isNew, err := loadOrGeneratePKI()
	if err != nil {
		return fmt.Errorf("failed to generate Mesh CA: %w", err)
	}

	if !isNew {
		return fmt.Errorf("failed to generate Mesh CA: loaded existing")
	}

	_, _ = fmt.Fprintf(out, "Mesh CA successfully generated at %s\n", path)
	return nil
}

func runPKIIssue(out io.Writer, nodeID string, outFile string) error {
	pki, isNew, err := loadOrGeneratePKI()
	if err != nil {
		return fmt.Errorf("failed to load Mesh CA: %w", err)
	}

	if isNew {
		return fmt.Errorf("mesh CA did not exist, please generate it first")
	}

	bundle, err := pki.generateNodeBundle(nodeID)
	if err != nil {
		return fmt.Errorf("failed to generate node bundle: %w", err)
	}

	ident := NodeIdentity{
		NodeID:  nodeID,
		CertPEM: bundle.CertPEM,
		KeyPEM:  bundle.KeyPEM,
		CaPEM:   bundle.CaPEM,
	}

	data, err := json.MarshalIndent(ident, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal identity: %w", err)
	}

	if outFile != "" {
		if err := os.WriteFile(outFile, data, 0600); err != nil {
			return fmt.Errorf("failed to write output: %w", err)
		}
		_, _ = fmt.Fprintf(out, "Node certificate bundle for '%s' saved to %s\n", nodeID, outFile)
	} else {
		_, _ = fmt.Fprintf(out, "%s\n", string(data))
	}

	return nil
}

func runPKIShow(out io.Writer) error {
	path, err := caPath()
	if err != nil {
		return fmt.Errorf("failed to determine CA path: %w", err)
	}

	pki, isNew, err := loadOrGeneratePKI()
	if err != nil {
		return fmt.Errorf("failed to load mesh CA: %w", err)
	}

	if isNew {
		return fmt.Errorf("mesh CA does not exist at %s", path)
	}

	_, _ = fmt.Fprintf(out, "Mesh CA Status: Active\n")
	_, _ = fmt.Fprintf(out, "Location: %s\n", path)

	if pki.ca != nil {
		_, _ = fmt.Fprintf(out, "Subject: %s\n", pki.ca.Subject.String())
		_, _ = fmt.Fprintf(out, "Issuer: %s\n", pki.ca.Issuer.String())
		_, _ = fmt.Fprintf(out, "Not Before: %s\n", pki.ca.NotBefore.Format("2006-01-02 15:04:05 Z0700 MST"))
		_, _ = fmt.Fprintf(out, "Not After: %s\n", pki.ca.NotAfter.Format("2006-01-02 15:04:05 Z0700 MST"))

		keyUsage := ""
		if pki.ca.KeyUsage&x509.KeyUsageCertSign != 0 {
			keyUsage += "CertSign "
		}
		if pki.ca.KeyUsage&x509.KeyUsageCRLSign != 0 {
			keyUsage += "CRLSign "
		}
		_, _ = fmt.Fprintf(out, "Key Usage: %s\n", keyUsage)
	}

	return nil
}
