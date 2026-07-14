package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/leelsey/gori"
)

// runConfig dispatches the "gori config" management subcommands.
func runConfig(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "gori config: subcommand required (init|path|show|validate|edit|add-provider|rm-provider|add-agent|rm-agent|set-default)")
		return 2
	}
	switch args[0] {
	case "-h", "--help", "help":
		fmt.Fprintln(stdout, "gori config <init|path|show|validate|edit|add-provider|rm-provider|add-agent|rm-agent|set-default>")
		return 0
	case "path":
		return configPathCmd(args[1:], stdout, stderr)
	case "init":
		return configInit(args[1:], stdout, stderr)
	case "show":
		return configShow(args[1:], stdout, stderr)
	case "validate":
		return configValidate(args[1:], stdout, stderr)
	case "edit":
		return configEdit(args[1:], stdout, stderr)
	case "add-provider":
		return configAddProvider(args[1:], stdout, stderr)
	case "rm-provider":
		return configRmProvider(args[1:], stdout, stderr)
	case "add-agent":
		return configAddAgent(args[1:], stdout, stderr)
	case "rm-agent":
		return configRmAgent(args[1:], stdout, stderr)
	case "set-default":
		return configSetDefault(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "gori config: unknown subcommand %q\n", args[0])
		return 2
	}
}

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}

func userConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "gori", "config.json"), nil
}

// discoverConfigPath finds an existing config for read/run, in order:
// $GORI_CONFIG, ./gori.json, ~/.config/gori/config.json.
func discoverConfigPath() (string, bool) {
	if p := os.Getenv("GORI_CONFIG"); p != "" && fileExists(p) {
		return p, true
	}
	if fileExists("gori.json") {
		return "gori.json", true
	}
	if p, err := userConfigPath(); err == nil && fileExists(p) {
		return p, true
	}
	return "", false
}

// configTargetPath resolves where a config command reads/writes when no explicit
// --config is given: $GORI_CONFIG, then an existing ./gori.json, then the default
// ~/.config/gori/config.json (created on save).
func configTargetPath(explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	if p := os.Getenv("GORI_CONFIG"); p != "" {
		return p, nil
	}
	if fileExists("gori.json") {
		return "gori.json", nil
	}
	return userConfigPath()
}

func loadOrEmpty(path string) (*gori.Config, error) {
	if fileExists(path) {
		return gori.LoadConfig(path)
	}
	return &gori.Config{Providers: []gori.ProviderConfig{}, Agents: []gori.PersonaConfig{}}, nil
}

func loadExisting(path string, stderr io.Writer) (*gori.Config, bool) {
	if !fileExists(path) {
		fmt.Fprintf(stderr, "gori config: %s not found (run 'gori config init')\n", path)
		return nil, false
	}
	c, err := gori.LoadConfig(path)
	if err != nil {
		fmt.Fprintln(stderr, "gori config:", err)
		return nil, false
	}
	return c, true
}

func configPathCmd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("gori config path", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", "", "config path override")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	p, err := configTargetPath(*cfgPath)
	if err != nil {
		fmt.Fprintln(stderr, "gori config:", err)
		return 1
	}
	status := "missing"
	if fileExists(p) {
		status = "exists"
	}
	fmt.Fprintf(stdout, "%s (%s)\n", p, status)
	return 0
}

func configInit(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("gori config init", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", "", "config path override")
	force := fs.Bool("force", false, "overwrite an existing config")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	p, err := configTargetPath(*cfgPath)
	if err != nil {
		fmt.Fprintln(stderr, "gori config:", err)
		return 1
	}
	if fileExists(p) && !*force {
		fmt.Fprintf(stderr, "gori config: %s already exists (use --force to overwrite)\n", p)
		return 1
	}
	c := &gori.Config{Providers: []gori.ProviderConfig{}, Agents: []gori.PersonaConfig{}}
	if err := c.Save(p); err != nil {
		fmt.Fprintln(stderr, "gori config:", err)
		return 1
	}
	fmt.Fprintf(stdout, "created %s\n", p)
	return 0
}

func configShow(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("gori config show", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", "", "config path override")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	p, err := configTargetPath(*cfgPath)
	if err != nil {
		fmt.Fprintln(stderr, "gori config:", err)
		return 1
	}
	c, ok := loadExisting(p, stderr)
	if !ok {
		return 1
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		fmt.Fprintln(stderr, "gori config:", err)
		return 1
	}
	fmt.Fprintln(stdout, string(b))
	return 0
}

func configValidate(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("gori config validate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", "", "config path override")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	p, err := configTargetPath(*cfgPath)
	if err != nil {
		fmt.Fprintln(stderr, "gori config:", err)
		return 1
	}
	if _, ok := loadExisting(p, stderr); !ok {
		return 1
	}
	fmt.Fprintf(stdout, "%s is valid\n", p)
	return 0
}

func configEdit(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("gori config edit", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", "", "config path override")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	p, err := configTargetPath(*cfgPath)
	if err != nil {
		fmt.Fprintln(stderr, "gori config:", err)
		return 1
	}
	c, err := loadOrEmpty(p)
	if err != nil {
		fmt.Fprintln(stderr, "gori config:", err)
		return 1
	}
	tmp := p + ".tmp"
	if err := c.Save(tmp); err != nil {
		fmt.Fprintln(stderr, "gori config:", err)
		return 1
	}
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vi"
	}
	cmd := exec.Command(editor, tmp)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		_ = os.Remove(tmp)
		fmt.Fprintln(stderr, "gori config: editor:", err)
		return 1
	}
	if _, err := gori.LoadConfig(tmp); err != nil {
		fmt.Fprintln(stderr, "gori config: edits invalid, original left unchanged:", err)
		fmt.Fprintf(stderr, "your draft is kept at %s\n", tmp)
		return 1
	}
	if err := os.Rename(tmp, p); err != nil {
		fmt.Fprintln(stderr, "gori config:", err)
		return 1
	}
	fmt.Fprintf(stdout, "saved %s\n", p)
	return 0
}

func configAddProvider(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("gori config add-provider", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", "", "config path override")
	name := fs.String("name", "", "provider name")
	ptype := fs.String("type", "", "anthropic|openai|google|cli")
	keyEnv := fs.String("api-key-env", "", "env var holding the API key")
	keyCmd := fs.String("api-key-cmd", "", "command that prints the API key")
	baseURL := fs.String("base-url", "", "override API base URL")
	command := fs.String("command", "", "cli backend command (type cli)")
	promptVia := fs.String("prompt-via", "", "stdin|arg (cli backend)")
	var cargs stringList
	fs.Var(&cargs, "arg", "cli backend argument (repeatable)")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if *name == "" || *ptype == "" {
		fmt.Fprintln(stderr, "gori config add-provider: --name and --type are required")
		return 2
	}
	p, err := configTargetPath(*cfgPath)
	if err != nil {
		fmt.Fprintln(stderr, "gori config:", err)
		return 1
	}
	c, err := loadOrEmpty(p)
	if err != nil {
		fmt.Fprintln(stderr, "gori config:", err)
		return 1
	}
	pc := gori.ProviderConfig{
		Name:      *name,
		Type:      *ptype,
		APIKeyEnv: *keyEnv,
		APIKeyCmd: *keyCmd,
		BaseURL:   *baseURL,
		Command:   *command,
		Args:      cargs,
		PromptVia: *promptVia,
	}
	verb := "added"
	replaced := false
	for i := range c.Providers {
		if c.Providers[i].Name == *name {
			c.Providers[i] = pc
			replaced = true
			verb = "updated"
			break
		}
	}
	if !replaced {
		c.Providers = append(c.Providers, pc)
	}
	if err := c.Save(p); err != nil {
		fmt.Fprintln(stderr, "gori config:", err)
		return 1
	}
	fmt.Fprintf(stdout, "%s provider %q in %s\n", verb, *name, p)
	return 0
}

func configRmProvider(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("gori config rm-provider", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", "", "config path override")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fmt.Fprintln(stderr, "gori config rm-provider: exactly one provider name required")
		return 2
	}
	name := rest[0]
	p, err := configTargetPath(*cfgPath)
	if err != nil {
		fmt.Fprintln(stderr, "gori config:", err)
		return 1
	}
	c, ok := loadExisting(p, stderr)
	if !ok {
		return 1
	}
	kept := make([]gori.ProviderConfig, 0, len(c.Providers))
	found := false
	for _, pr := range c.Providers {
		if pr.Name == name {
			found = true
			continue
		}
		kept = append(kept, pr)
	}
	if !found {
		fmt.Fprintf(stderr, "gori config: provider %q not found\n", name)
		return 1
	}
	c.Providers = kept
	if err := c.Save(p); err != nil {
		fmt.Fprintln(stderr, "gori config:", err)
		return 1
	}
	fmt.Fprintf(stdout, "removed provider %q from %s\n", name, p)
	return 0
}

func configAddAgent(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("gori config add-agent", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", "", "config path override")
	name := fs.String("name", "", "agent name")
	provider := fs.String("provider", "", "provider name (must exist)")
	model := fs.String("model", "", "model name")
	system := fs.String("system", "", "system prompt")
	role := fs.String("role", "", "main|sub")
	thinking := fs.String("thinking", "", "off|auto|budget")
	thinkingBudget := fs.Int("thinking-budget", 0, "thinking budget tokens")
	maxTokens := fs.Int("max-tokens", 0, "max output tokens")
	temperature := fs.Float64("temperature", 0, "sampling temperature")
	description := fs.String("description", "", "shown to the main agent when delegated")
	setDefault := fs.Bool("default", false, "also set as default_agent")
	var respModalities stringList
	fs.Var(&respModalities, "response-modality", "request non-text output: audio | image (repeatable)")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if *name == "" || *provider == "" || *model == "" {
		fmt.Fprintln(stderr, "gori config add-agent: --name, --provider and --model are required")
		return 2
	}
	p, err := configTargetPath(*cfgPath)
	if err != nil {
		fmt.Fprintln(stderr, "gori config:", err)
		return 1
	}
	c, err := loadOrEmpty(p)
	if err != nil {
		fmt.Fprintln(stderr, "gori config:", err)
		return 1
	}
	pa := gori.PersonaConfig{
		Name:               *name,
		Provider:           *provider,
		Model:              *model,
		System:             *system,
		Role:               *role,
		Description:        *description,
		MaxTokens:          *maxTokens,
		Temperature:        *temperature,
		Thinking:           *thinking,
		ThinkingBudget:     *thinkingBudget,
		ResponseModalities: respModalities,
	}
	verb := "added"
	replaced := false
	for i := range c.Agents {
		if c.Agents[i].Name == *name {
			c.Agents[i] = pa
			replaced = true
			verb = "updated"
			break
		}
	}
	if !replaced {
		c.Agents = append(c.Agents, pa)
	}
	if *setDefault {
		c.DefaultAgent = *name
	}
	if err := c.Save(p); err != nil {
		fmt.Fprintln(stderr, "gori config:", err)
		return 1
	}
	fmt.Fprintf(stdout, "%s agent %q in %s\n", verb, *name, p)
	return 0
}

func configRmAgent(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("gori config rm-agent", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", "", "config path override")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fmt.Fprintln(stderr, "gori config rm-agent: exactly one agent name required")
		return 2
	}
	name := rest[0]
	p, err := configTargetPath(*cfgPath)
	if err != nil {
		fmt.Fprintln(stderr, "gori config:", err)
		return 1
	}
	c, ok := loadExisting(p, stderr)
	if !ok {
		return 1
	}
	kept := make([]gori.PersonaConfig, 0, len(c.Agents))
	found := false
	for _, a := range c.Agents {
		if a.Name == name {
			found = true
			continue
		}
		kept = append(kept, a)
	}
	if !found {
		fmt.Fprintf(stderr, "gori config: agent %q not found\n", name)
		return 1
	}
	c.Agents = kept
	if c.DefaultAgent == name {
		c.DefaultAgent = ""
	}
	if err := c.Save(p); err != nil {
		fmt.Fprintln(stderr, "gori config:", err)
		return 1
	}
	fmt.Fprintf(stdout, "removed agent %q from %s\n", name, p)
	return 0
}

func configSetDefault(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("gori config set-default", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", "", "config path override")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	rest := fs.Args()
	if len(rest) != 1 {
		fmt.Fprintln(stderr, "gori config set-default: exactly one agent name required")
		return 2
	}
	name := rest[0]
	p, err := configTargetPath(*cfgPath)
	if err != nil {
		fmt.Fprintln(stderr, "gori config:", err)
		return 1
	}
	c, ok := loadExisting(p, stderr)
	if !ok {
		return 1
	}
	c.DefaultAgent = name
	if err := c.Save(p); err != nil {
		fmt.Fprintln(stderr, "gori config:", err)
		return 1
	}
	fmt.Fprintf(stdout, "default_agent set to %q in %s\n", name, p)
	return 0
}
