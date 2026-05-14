package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	ipkg "github.com/confighub/installer/internal/pkg"
)

func newLoginCmd() *cobra.Command {
	var (
		username      string
		password      string
		passwordStdin bool
	)
	cmd := &cobra.Command{
		Use:   "login <registry>",
		Short: "Store credentials for a registry",
		Long: `Login stores a credential for the given registry in the docker-config-style
credential store (typically ~/.docker/config.json), making it usable by
installer push, pull, inspect, and list — plus any other tool that reads
docker config (docker, podman, oras).

If --username is omitted, it is read from stdin. If neither --password nor
--password-stdin is set, the password is read interactively from /dev/tty
without echo.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			registry := args[0]
			if username == "" {
				u, err := promptLine("Username: ")
				if err != nil {
					return err
				}
				username = strings.TrimSpace(u)
			}
			if passwordStdin {
				if password != "" {
					return fmt.Errorf("--password and --password-stdin are mutually exclusive")
				}
				b, err := io.ReadAll(os.Stdin)
				if err != nil {
					return err
				}
				password = strings.TrimRight(string(b), "\r\n")
			} else if password == "" {
				p, err := promptPassword("Password: ")
				if err != nil {
					return err
				}
				password = p
			}
			ctx := context.Background()
			if err := ipkg.Login(ctx, registry, username, password); err != nil {
				return err
			}
			fmt.Fprintf(os.Stderr, "Login succeeded for %s\n", registry)
			return nil
		},
	}
	cmd.Flags().StringVarP(&username, "username", "u", "", "username")
	cmd.Flags().StringVarP(&password, "password", "p", "", "password (avoid; prefer --password-stdin)")
	cmd.Flags().BoolVar(&passwordStdin, "password-stdin", false, "read password from stdin")
	return cmd
}

func promptLine(prompt string) (string, error) {
	fmt.Fprint(os.Stderr, prompt)
	r := bufio.NewReader(os.Stdin)
	line, err := r.ReadString('\n')
	if err != nil && err != io.EOF {
		return "", err
	}
	return line, nil
}

func promptPassword(prompt string) (string, error) {
	fmt.Fprint(os.Stderr, prompt)
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		// Non-interactive: read from stdin until newline.
		line, err := promptLine("")
		return strings.TrimRight(line, "\r\n"), err
	}
	b, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	return string(b), err
}
