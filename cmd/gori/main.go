// Command gori is the standalone CLI for the gori agent framework.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/leelsey/gori"
	"github.com/leelsey/gori/a2a"
	"github.com/leelsey/gori/internal/build"
	"github.com/leelsey/gori/internal/rpc"
	"github.com/leelsey/gori/internal/tui"
	"github.com/leelsey/gori/mcp"
	"github.com/leelsey/gori/netbus"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) > 0 {
		switch args[0] {
		case "mcp-server":
			return runMCPServer(args[1:], stderr)
		case "a2a-serve":
			return runA2AServe(args[1:], stderr)
		case "bus":
			return runBus(args[1:], stderr)
		case "tui":
			return runTUI(args[1:], stdin, stdout, stderr)
		case "config":
			return runConfig(args[1:], stdin, stdout, stderr)
		case "help", "-h", "--help":
			usage(stdout)
			return 0
		}
	}
	fs := flag.NewFlagSet("gori", flag.ContinueOnError)
	fs.SetOutput(stderr)
	bf := addBackendFlags(fs)
	var showVer bool
	fs.BoolVar(&showVer, "version", false, "print version and exit")
	fs.BoolVar(&showVer, "V", false, "print version and exit (shorthand)")
	var (
		noStream    = fs.Bool("no-stream", false, "disable streaming output")
		orchestrate = fs.Bool("orchestrate", false, "run multi-agent orchestration from --config")
		showUsage   = fs.Bool("usage", false, "print token usage to stderr after the run")
	)
	var images, audios stringList
	fs.Var(&images, "image", "image file to attach as input (repeatable)")
	fs.Var(&audios, "audio", "audio file to attach as input (repeatable)")
	fs.Usage = func() { usage(stderr) }
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if showVer {
		fmt.Fprintln(stdout, "gori", gori.Version)
		return 0
	}

	prompt := strings.TrimSpace(strings.Join(fs.Args(), " "))
	hasAttach := len(images) > 0 || len(audios) > 0
	if prompt == "" && !hasAttach {
		if isTerminal(stdin) {
			usage(stdout)
			return 0
		}
		data, _ := io.ReadAll(stdin)
		prompt = strings.TrimSpace(string(data))
	}
	if prompt == "" && !hasAttach {
		fmt.Fprintln(stderr, "gori: no prompt (pass arguments, pipe via stdin, attach --image/--audio, or run 'gori tui')")
		return 2
	}

	ctx, stop := bf.runContext()
	defer stop()

	cfgPath := bf.resolveConfigPath(stderr)
	if !validModalities(bf.modalities, stderr) {
		return 2
	}

	if *orchestrate {
		if len(images) > 0 || len(audios) > 0 || len(bf.modalities) > 0 {
			fmt.Fprintln(stderr, "gori: warning: --image/--audio/--modality are ignored in --orchestrate mode")
		}
		return runOrchestrator(ctx, cfgPath, prompt, *showUsage, stdout, stderr)
	}

	agent, err := bf.buildAgent(cfgPath)
	if err != nil {
		fmt.Fprintln(stderr, "gori:", err)
		return 1
	}

	userMsg, err := buildUserMessage(prompt, images, audios)
	if err != nil {
		fmt.Fprintln(stderr, "gori:", err)
		return 1
	}

	warnUnsupportedAttachments(stderr, agent.Provider.Name(), agent.Provider.Capabilities(), images, audios)

	if *noStream {
		msg, err := agent.RunMessage(ctx, userMsg)
		if err != nil {
			if *showUsage {
				printAgentUsage(stderr, agent) // failed runs still billed completed steps
			}
			return exitErr(stderr, ctx, err)
		}
		fmt.Fprintln(stdout, msg.Text())
		saveMedia(msg, stderr)
		if *showUsage {
			printAgentUsage(stderr, agent)
		}
		return 0
	}

	out, err := agent.StreamMessage(ctx, userMsg, func(ev gori.StreamEvent) error {
		switch ev.Type {
		case gori.EventTextDelta:
			fmt.Fprint(stdout, ev.Text)
		case gori.EventThinkingDelta:
			fmt.Fprint(stderr, ev.Text)
		}
		return nil
	})
	fmt.Fprintln(stdout) // terminate the streamed line before anything else prints
	if *showUsage {
		printAgentUsage(stderr, agent)
	}
	if err != nil {
		return exitErr(stderr, ctx, err)
	}
	saveMedia(out, stderr)
	return 0
}

// formatUsage renders a Usage as a single human-readable line fragment.
func formatUsage(u gori.Usage) string { return "tokens: " + u.String() }

// printAgentUsage prints per-step usage (when the run took more than one
// provider call) followed by the run total.
func printAgentUsage(w io.Writer, a *gori.Agent) {
	if len(a.StepUsage) > 1 {
		for i, u := range a.StepUsage {
			fmt.Fprintf(w, "gori: step %d %s\n", i+1, formatUsage(u))
		}
	}
	fmt.Fprintf(w, "gori: %s\n", formatUsage(a.TotalUsage))
}

// backendFlags are the provider/agent flags shared by `gori` and `gori tui`.
type backendFlags struct {
	model, provider, cli, baseURL, apiKeyEnv, config, agent, system, think string
	timeout                                                                time.Duration
	modalities                                                             stringList
}

func addBackendFlags(fs *flag.FlagSet) *backendFlags {
	bf := &backendFlags{}
	fs.StringVar(&bf.model, "model", "", "model name")
	fs.StringVar(&bf.model, "m", "", "model name (shorthand)")
	fs.StringVar(&bf.provider, "provider", "anthropic", "provider type: anthropic|openai|google")
	fs.StringVar(&bf.cli, "cli", "", `external agentic CLI backend, e.g. --cli "claude -p"`)
	fs.StringVar(&bf.baseURL, "base-url", "", "override provider API base URL (e.g. local OpenAI-compatible server)")
	fs.StringVar(&bf.apiKeyEnv, "api-key-env", "", "env var to read the API key from (default: provider's standard var)")
	fs.StringVar(&bf.config, "config", "", "path to JSON config")
	fs.StringVar(&bf.agent, "agent", "", "agent name (requires --config)")
	fs.StringVar(&bf.system, "system", "", "system prompt (ad-hoc mode)")
	fs.StringVar(&bf.think, "think", "", "thinking mode: off|auto|budget (default off)")
	fs.DurationVar(&bf.timeout, "timeout", 0, "overall deadline, e.g. 90s or 2m (0: none)")
	fs.Var(&bf.modalities, "modality", "request non-text output: audio | image (repeatable)")
	return bf
}

// resolveConfigPath applies auto-discovery and the ignored-flag warning;
// discovery yields to ad-hoc backend flags unless --agent asks for the config.
func (bf *backendFlags) resolveConfigPath(stderr io.Writer) string {
	cfgPath := bf.config
	adHoc := bf.cli != "" || bf.model != "" || bf.baseURL != "" || bf.apiKeyEnv != ""
	if cfgPath == "" && (bf.agent != "" || !adHoc) {
		if p, ok := discoverConfigPath(); ok {
			cfgPath = p
		}
	}
	if cfgPath != "" {
		warnIgnoredConfigFlags(stderr, bf.cli, bf.provider, bf.baseURL, bf.apiKeyEnv)
	}
	return cfgPath
}

func (bf *backendFlags) buildAgent(cfgPath string) (*gori.Agent, error) {
	return buildAgent(cfgPath, bf.agent, bf.provider, bf.model, bf.system, bf.think, bf.cli, bf.baseURL, bf.apiKeyEnv, bf.modalities)
}

// runContext returns a ctx cancelled by SIGINT/SIGTERM and, when --timeout is
// set, a deadline.
func (bf *backendFlags) runContext() (context.Context, context.CancelFunc) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	if bf.timeout > 0 {
		tctx, cancel := context.WithTimeout(ctx, bf.timeout)
		return tctx, func() { cancel(); stop() }
	}
	return ctx, stop
}

// exitErr maps a run error to an exit status: our own signal cancellation exits
// quietly with 130; anything else is printed and exits 1.
func exitErr(stderr io.Writer, ctx context.Context, err error) int {
	if errors.Is(err, context.Canceled) && errors.Is(ctx.Err(), context.Canceled) {
		return 130
	}
	fmt.Fprintln(stderr, "gori:", err)
	return 1
}

// warnIgnoredConfigFlags warns when config mode drops ad-hoc backend flags.
func warnIgnoredConfigFlags(stderr io.Writer, cliCmd, providerType, baseURL, apiKeyEnv string) {
	var ignored []string
	if cliCmd != "" {
		ignored = append(ignored, "--cli")
	}
	if providerType != "" && providerType != "anthropic" {
		ignored = append(ignored, "--provider")
	}
	if baseURL != "" {
		ignored = append(ignored, "--base-url")
	}
	if apiKeyEnv != "" {
		ignored = append(ignored, "--api-key-env")
	}
	if len(ignored) > 0 {
		fmt.Fprintf(stderr, "gori: warning: %s ignored in --config mode (config defines the provider)\n", strings.Join(ignored, ", "))
	}
}

func buildAgent(configPath, agentName, providerType, model, system, think, cliCmd, baseURL, apiKeyEnv string, modalities []string) (*gori.Agent, error) {
	if configPath == "" && agentName != "" {
		return nil, fmt.Errorf("--agent requires a config (pass --config or run where gori.json is discoverable)")
	}
	if configPath != "" {
		cfg, err := gori.LoadConfig(configPath)
		if err != nil {
			return nil, err
		}
		name := agentName
		if name == "" {
			name = cfg.DefaultAgent
		}
		if name == "" {
			return nil, fmt.Errorf("--agent required (or set default_agent in config)")
		}
		agent, err := build.Agent(cfg, name, gori.NewRegistry())
		if err != nil {
			return nil, err
		}
		if model != "" {
			agent.Model = model
		}
		if system != "" {
			agent.System = system
		}
		// unset --think preserves the persona's thinking; "off" overrides it
		if think != "" {
			agent.Thinking = parseThink(think)
		}
		if len(modalities) > 0 {
			agent.ResponseModalities = modalities
		}
		return agent, nil
	}

	if cliCmd != "" {
		fields := splitArgs(cliCmd)
		if len(fields) == 0 {
			return nil, fmt.Errorf("--cli command is empty")
		}
		prov, err := build.Provider(gori.ProviderConfig{
			Name:      "cli",
			Type:      "cli",
			Command:   fields[0],
			Args:      fields[1:],
			PromptVia: "stdin",
		})
		if err != nil {
			return nil, err
		}
		return &gori.Agent{
			Provider:           prov,
			Model:              "cli",
			System:             system,
			Session:            gori.NewSession(),
			ResponseModalities: modalities,
		}, nil
	}

	if model == "" {
		return nil, fmt.Errorf("--model is required in ad-hoc mode (or use --config / --cli)")
	}
	keyEnv := defaultKeyEnv(providerType)
	if apiKeyEnv != "" {
		keyEnv = apiKeyEnv
	}
	prov, err := build.Provider(gori.ProviderConfig{
		Name:      providerType,
		Type:      providerType,
		APIKeyEnv: keyEnv,
		BaseURL:   baseURL,
	})
	if err != nil {
		return nil, err
	}
	return &gori.Agent{
		Provider:           prov,
		Model:              model,
		System:             system,
		Session:            gori.NewSession(),
		Thinking:           parseThink(think),
		ResponseModalities: modalities,
	}, nil
}

func runOrchestrator(ctx context.Context, configPath, prompt string, showUsage bool, stdout, stderr io.Writer) int {
	if configPath == "" {
		fmt.Fprintln(stderr, "gori: --orchestrate requires --config")
		return 1
	}
	cfg, err := gori.LoadConfig(configPath)
	if err != nil {
		fmt.Fprintln(stderr, "gori:", err)
		return 1
	}

	bus := gori.NewBus()
	var bridgeDone chan struct{}
	if cfg.Bus != "" {
		nc := netbus.NewClient(cfg.Bus)
		if cfg.BusTokenEnv != "" {
			token := os.Getenv(cfg.BusTokenEnv)
			if token == "" {
				// Fail fast (like gori bus --token-env): bridging tokenless
				// against a protected hub would 401 every event silently.
				fmt.Fprintf(stderr, "gori: bus_token_env %s is empty or unset\n", cfg.BusTokenEnv)
				return 1
			}
			nc = nc.WithToken(token)
		}
		bridgeDone = make(chan struct{})
		go func() { defer close(bridgeDone); _ = nc.Bridge(ctx, bus) }()
		fmt.Fprintf(stderr, "gori: bridging events to network bus %s\n", cfg.Bus)
	}
	events, unsub := bus.Subscribe("*")
	defer unsub()
	done := make(chan struct{})
	go func() {
		for ev := range events {
			fmt.Fprintf(stderr, "[%s] %s\n", ev.Agent, ev.Kind)
		}
		close(done)
	}()

	o, err := build.Orchestrator(cfg, bus, gori.NewRegistry())
	if err != nil {
		bus.Close()
		<-done
		fmt.Fprintln(stderr, "gori:", err)
		return 1
	}

	mcpTools, closeMCP, mErr := build.MCPTools(ctx, cfg)
	if mErr != nil {
		bus.Close()
		<-done
		fmt.Fprintln(stderr, "gori:", mErr)
		return 1
	}
	defer closeMCP()
	if main := o.Main(); main != nil && len(mcpTools) > 0 {
		main.Tools.Register(mcpTools...)
		fmt.Fprintf(stderr, "gori: added %d tool(s) from MCP servers\n", len(mcpTools))
	}
	if atools := build.A2ATools(cfg); len(atools) > 0 {
		if main := o.Main(); main != nil {
			main.Tools.Register(atools...)
			fmt.Fprintf(stderr, "gori: added %d remote A2A agent tool(s)\n", len(atools))
		}
	}

	out, err := o.Run(ctx, prompt)
	if showUsage {
		if main := o.Main(); main != nil {
			printAgentUsage(stderr, main)
		}
		if du := o.Usage(); du != (gori.Usage{}) {
			fmt.Fprintf(stderr, "gori: delegated %s\n", formatUsage(du))
		}
	}
	bus.Close()
	<-done
	if bridgeDone != nil {
		// bounded wait so the run's final events reach the hub before exit
		select {
		case <-bridgeDone:
		case <-time.After(3 * time.Second):
			fmt.Fprintln(stderr, "gori: warning: bus bridge did not drain in time; final events may be lost")
		}
	}
	if err != nil {
		return exitErr(stderr, ctx, err)
	}
	fmt.Fprintln(stdout, out.Text())
	return 0
}

func runMCPServer(args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("gori mcp-server", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", "", "config whose agents are exposed as MCP tools")
	name := fs.String("name", "gori", "MCP server name")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	srv := mcp.NewServer(*name, gori.Version)
	if *configPath != "" {
		cfg, err := gori.LoadConfig(*configPath)
		if err != nil {
			fmt.Fprintln(stderr, "gori mcp-server:", err)
			return 1
		}
		for _, p := range cfg.Agents {
			ag, err := build.Agent(cfg, p.Name, gori.NewRegistry())
			if err != nil {
				fmt.Fprintln(stderr, "gori mcp-server:", err)
				return 1
			}
			desc := p.Description
			if desc == "" {
				desc = "Run the " + p.Name + " agent."
			}
			srv.AddAgent(p.Name, desc, ag)
		}
		fmt.Fprintf(stderr, "gori mcp-server: exposing %d agent(s) over stdio\n", len(cfg.Agents))
	} else {
		fmt.Fprintln(stderr, "gori mcp-server: no -config; serving with no tools")
	}
	if err := srv.Serve(context.Background(), rpc.Stdio()); err != nil {
		fmt.Fprintln(stderr, "gori mcp-server:", err)
		return 1
	}
	return 0
}

func runA2AServe(args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("gori a2a-serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	configPath := fs.String("config", "", "config defining the agent to expose")
	agentName := fs.String("agent", "", "agent to expose (default: default_agent or first)")
	addr := fs.String("addr", ":8080", "listen address")
	urlFlag := fs.String("url", "", "advertised agent-card URL (default derived from -addr)")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if *configPath == "" {
		fmt.Fprintln(stderr, "gori a2a-serve: --config is required")
		return 1
	}
	cfg, err := gori.LoadConfig(*configPath)
	if err != nil {
		fmt.Fprintln(stderr, "gori a2a-serve:", err)
		return 1
	}
	name := *agentName
	if name == "" {
		name = cfg.DefaultAgent
	}
	if name == "" && len(cfg.Agents) > 0 {
		name = cfg.Agents[0].Name
	}
	if name == "" {
		fmt.Fprintln(stderr, "gori a2a-serve: no agent to expose")
		return 1
	}
	ag, err := build.Agent(cfg, name, gori.NewRegistry())
	if err != nil {
		fmt.Fprintln(stderr, "gori a2a-serve:", err)
		return 1
	}
	persona, _ := cfg.Agent(name)
	desc := persona.Description
	if desc == "" {
		desc = "gori agent " + name
	}
	url := *urlFlag
	if url == "" {
		url = "http://localhost" + *addr
		if !strings.HasPrefix(*addr, ":") {
			url = "http://" + *addr
		}
	}
	card := a2a.CardForAgent(name, desc, url+"/")
	srv := a2a.NewServer(card, a2a.AgentHandler(ag))
	fmt.Fprintf(stderr, "gori a2a-serve: exposing agent %q at %s (card: %s%s)\n", name, *addr, url, "/.well-known/agent-card.json")
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := runHTTPServer(ctx, *addr, srv.HTTPHandler()); err != nil {
		fmt.Fprintln(stderr, "gori a2a-serve:", err)
		return 1
	}
	return 0
}

// runHTTPServer serves h on addr until ctx is cancelled, then
// shuts down gracefully, draining in-flight requests. No WriteTimeout is set so
// long-lived SSE streams are not cut; ReadHeaderTimeout/IdleTimeout bound slow
// or idle connections.
func runHTTPServer(ctx context.Context, addr string, h http.Handler, onShutdown ...func()) error {
	srv := &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	for _, f := range onShutdown {
		srv.RegisterOnShutdown(f)
	}
	errCh := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
		close(errCh)
	}()
	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		shErr := srv.Shutdown(shutdownCtx)
		// Prefer a listen/bind error that raced in just before cancellation; errCh
		// is closed once ListenAndServe returns, so this never blocks indefinitely.
		if lErr := <-errCh; lErr != nil {
			return lErr
		}
		return shErr
	}
}

func runBus(args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("gori bus", flag.ContinueOnError)
	fs.SetOutput(stderr)
	addr := fs.String("addr", ":7777", "listen address")
	tokenEnv := fs.String("token-env", "", "env var holding a bearer token required on every request")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	hub := netbus.NewHub()
	if *tokenEnv != "" {
		hub.AuthToken = os.Getenv(*tokenEnv)
		if hub.AuthToken == "" {
			fmt.Fprintf(stderr, "gori bus: --token-env %s is empty or unset\n", *tokenEnv)
			return 2
		}
	}
	fmt.Fprintf(stderr, "gori bus: hub on %s (POST /publish, GET /subscribe)\n", *addr)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := runHTTPServer(ctx, *addr, hub.Handler(), hub.CloseAll); err != nil {
		fmt.Fprintln(stderr, "gori bus:", err)
		return 1
	}
	return 0
}

type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

func warnUnsupportedAttachments(stderr io.Writer, name string, caps gori.Capabilities, images, audios stringList) {
	if len(images) > 0 && !caps.Images {
		fmt.Fprintf(stderr, "gori: warning: provider %q does not support image input; %d attachment(s) ignored\n", name, len(images))
	}
	if len(audios) > 0 && !caps.Audio {
		fmt.Fprintf(stderr, "gori: warning: provider %q does not support audio input; %d attachment(s) ignored\n", name, len(audios))
	}
}

func buildUserMessage(prompt string, images, audios stringList) (gori.Message, error) {
	msg := gori.Message{Role: gori.RoleUser}
	if prompt != "" {
		msg.Content = append(msg.Content, gori.Text{Text: prompt})
	}
	for _, p := range images {
		data, err := os.ReadFile(p)
		if err != nil {
			return gori.Message{}, err
		}
		msg.Content = append(msg.Content, gori.Image{MediaType: mediaTypeOf(p, "image/png"), Data: data})
	}
	for _, p := range audios {
		data, err := os.ReadFile(p)
		if err != nil {
			return gori.Message{}, err
		}
		msg.Content = append(msg.Content, gori.Audio{MediaType: mediaTypeOf(p, "audio/wav"), Data: data})
	}
	return msg, nil
}

func mediaTypeOf(path, fallback string) string {
	if mt := mime.TypeByExtension(filepath.Ext(path)); mt != "" {
		if i := strings.IndexByte(mt, ';'); i >= 0 {
			mt = strings.TrimSpace(mt[:i])
		}
		return mt
	}
	return fallback
}

func saveMedia(msg gori.Message, stderr io.Writer) {
	img, aud := 0, 0
	for _, c := range msg.Content {
		switch v := c.(type) {
		case gori.Image:
			img++
			writeMediaFile(stderr, "image", img, v.MediaType, v.Data)
		case gori.Audio:
			aud++
			writeMediaFile(stderr, "audio", aud, v.MediaType, v.Data)
		}
	}
}

func writeMediaFile(stderr io.Writer, kind string, n int, mediaType string, data []byte) {
	if len(data) == 0 {
		return
	}
	ext := extOf(mediaType)
	name := fmt.Sprintf("gori-%s-%d%s", kind, n, ext)
	for i := 1; fileExists(name); i++ {
		name = fmt.Sprintf("gori-%s-%d-%d%s", kind, n, i, ext) // avoid clobbering prior output
	}
	name = filepath.Base(name) // defence in depth: never escape the working directory
	if err := os.WriteFile(name, data, 0o644); err != nil {
		fmt.Fprintf(stderr, "gori: save %s: %v\n", name, err)
		return
	}
	fmt.Fprintf(stderr, "gori: saved %s output -> %s\n", kind, name)
}

func extOf(mediaType string) string {
	if i := strings.IndexByte(mediaType, ';'); i >= 0 {
		mediaType = strings.TrimSpace(mediaType[:i]) // drop parameters, e.g. audio/L16;rate=24000
	}
	if mediaType == "image/jpeg" {
		return ".jpg" // mime.ExtensionsByType sorts ".jfif" first; prefer the conventional ext
	}
	if exts, _ := mime.ExtensionsByType(mediaType); len(exts) > 0 {
		return exts[0]
	}
	if i := strings.IndexByte(mediaType, '/'); i >= 0 {
		if sub := mediaType[i+1:]; isAlnum(sub) {
			return "." + sub // provider-controlled: reject anything but [A-Za-z0-9] to avoid path traversal
		}
	}
	return ".bin"
}

// splitArgs splits a command string into argv, honouring quotes. It delegates to
// build.SplitArgs so the CLI and the api_key_cmd resolver split identically.
func splitArgs(s string) []string { return build.SplitArgs(s) }

// validModalities lowercases each value in place and reports the first
// unrecognised one ("text" is valid: Gemini image output needs TEXT+IMAGE).
func validModalities(mods []string, stderr io.Writer) bool {
	for i, m := range mods {
		switch l := strings.ToLower(m); l {
		case "text", "audio", "image":
			mods[i] = l
		default:
			fmt.Fprintf(stderr, "gori: invalid --modality %q (want text, audio or image)\n", m)
			return false
		}
	}
	return true
}

func isAlnum(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

func runTUI(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("gori tui", flag.ContinueOnError)
	fs.SetOutput(stderr)
	bf := addBackendFlags(fs)
	showUsage := fs.Bool("usage", false, "print token usage to stderr after each turn")
	fs.Usage = func() { usage(stderr) }
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	cfgPath := bf.resolveConfigPath(stderr)
	if !validModalities(bf.modalities, stderr) {
		return 2
	}
	agent, err := bf.buildAgent(cfgPath)
	if err != nil {
		fmt.Fprintln(stderr, "gori:", err)
		return 1
	}
	ctx, stop := bf.runContext()
	defer stop()
	// Ctrl-C and --timeout expiry are expected session endings, not failures.
	onFinal := func(m gori.Message) {
		saveMedia(m, stderr)
		if *showUsage {
			printAgentUsage(stderr, agent)
		}
	}
	switch err := tui.Run(ctx, agent, stdin, stdout, onFinal); {
	case err == nil || errors.Is(err, context.Canceled):
	case errors.Is(err, context.DeadlineExceeded):
		fmt.Fprintln(stderr, "gori: session ended (--timeout reached)")
	default:
		fmt.Fprintln(stderr, "gori:", err)
		return 1
	}
	return 0
}

func usage(w io.Writer) {
	fmt.Fprintf(w, `gori %s — lightweight LLM agent

Usage:
  gori [flags] "prompt"            one-shot prompt (ad-hoc or --config)
  gori tui [flags]                 interactive terminal session
  gori mcp-server [--config f]     serve tools/agents over MCP (stdio)
  gori a2a-serve [--config f]      serve an agent over A2A (HTTP)
  gori bus [--addr :7777] [--token-env V]  run a network-bus hub
  gori config <subcmd>             manage config (init/show/edit/add-*/rm-*/set-default)
  gori --version, -V
  gori --help, -h

Flags (run / tui):
  --model, -m <model>   model name
  --provider <name>     anthropic | openai | google (ad-hoc mode)
  --cli "<cmd>"         external agentic CLI as the model, e.g. "claude -p" (no API key)
  --base-url <url>      override provider base URL (e.g. http://localhost:11434/v1)
  --api-key-env <name>  env var to read the API key from (default: provider's standard var)
  --config <path>       JSON config file
  --agent <name>        agent defined in --config
  --system <text>       system prompt (ad-hoc mode)
  --think <mode>        off | auto | budget
  --image <file>        attach an image as input (repeatable)
  --audio <file>        attach audio as input (repeatable)
  --modality <name>     request non-text output: audio | image (repeatable)
  --no-stream           disable streaming output
  --usage               print token usage to stderr (run: after the run; tui: each turn)
  --orchestrate         multi-agent orchestration from --config
  --timeout <dur>       overall deadline, e.g. 90s or 2m (default: none)

Note: single-dash forms (-config) are still accepted.
Config search (when --config omitted): $GORI_CONFIG, ./gori.json, ~/.config/gori/config.json
Environment (API keys): ANTHROPIC_API_KEY, OPENAI_API_KEY, GEMINI_API_KEY
`, gori.Version)
}

func isTerminal(r io.Reader) bool {
	f, ok := r.(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

func defaultKeyEnv(providerType string) string {
	switch providerType {
	case "openai":
		return "OPENAI_API_KEY"
	case "google":
		return "GEMINI_API_KEY"
	default:
		return "ANTHROPIC_API_KEY"
	}
}

func parseThink(s string) gori.ThinkingConfig {
	switch s {
	case "auto":
		return gori.ThinkingConfig{Mode: gori.ThinkingAuto}
	case "budget":
		return gori.ThinkingConfig{Mode: gori.ThinkingBudget, Budget: 2048}
	default:
		return gori.ThinkingConfig{Mode: gori.ThinkingOff}
	}
}
