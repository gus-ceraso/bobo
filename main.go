package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"

	"ceraso.dev/bobo/exa"
	"ceraso.dev/bobo/openrouter"
	"ceraso.dev/bobo/repl"
	"ceraso.dev/bobo/tool"
)

var systemPrompt string = `You are a helpful assistant. Always answer the user in UTF-8 plain text (NO MARKDOWN).`

func main() {
	var disableWeb, enableExplictCaching, enableWrite, enableYOLO bool
	var model, effort, logFilePath string
	flag.BoolVar(&disableWeb, "f", false, "disable web search and fetch")
	flag.BoolVar(&enableExplictCaching, "c", false, "enable explict prompt caching")
	flag.BoolVar(&enableWrite, "w", false, "enable filesystem write")
	flag.BoolVar(&enableYOLO, "y", false, "enable YOLO mode")
	flag.StringVar(&model, "m", "~anthropic/claude-haiku-latest", "OpenRouter model slug. Defaults to ~anthropic/claude-haiku-latest")
	flag.StringVar(&effort, "e", "low", "OpenRouter reasoning effort. Defaults to low")
	flag.StringVar(&logFilePath, "l", "", "Path for verbose log file")
	flag.Parse()

	openrouterToken := os.Getenv("OPENROUTER_API_KEY")
	if openrouterToken == "" {
		fmt.Fprintln(os.Stderr, "bobo: missing OPENROUTER_API_KEY")
		os.Exit(1)
	}
	client := openrouter.Client{Token: openrouterToken}
	if len(logFilePath) > 0 {
		logFile, err := os.OpenFile(logFilePath, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
		if err != nil {
			fmt.Fprintf(os.Stderr, "bobo: open log file: %s\n", err)
			os.Exit(1)
		}
		defer logFile.Close()
		client.Log = log.New(logFile, "", log.Ldate|log.Ltime)
	}

	hiddenFiles := false
	localFiles := true
	if enableYOLO {
		hiddenFiles = true
		localFiles = false
	}
	tools := []openrouter.Tool{
		&tool.Read{HiddenFiles: hiddenFiles, LocalFiles: localFiles},
		&tool.Find{HiddenFiles: hiddenFiles, LocalFiles: localFiles},
		&tool.Grep{HiddenFiles: hiddenFiles, LocalFiles: localFiles},
	}
	if exaToken := os.Getenv("EXA_API_KEY"); len(exaToken) > 0 && !disableWeb {
		exaClient := exa.NewClient(exaToken)
		tools = append(tools, &tool.Search{Client: exaClient})
		tools = append(tools, &tool.Fetch{Client: exaClient})
	}
	if enableWrite || enableYOLO {
		tools = append(tools, &tool.Write{HiddenFiles: hiddenFiles, LocalFiles: localFiles})
		tools = append(tools, &tool.Edit{HiddenFiles: hiddenFiles, LocalFiles: localFiles})
		tools = append(tools, &tool.Copy{HiddenFiles: hiddenFiles, LocalFiles: localFiles})
		tools = append(tools, &tool.Move{HiddenFiles: hiddenFiles, LocalFiles: localFiles})
		tools = append(tools, &tool.Remove{HiddenFiles: hiddenFiles, LocalFiles: localFiles})
	}
	if enableYOLO {
		tools = append(tools, &tool.Exec{})
	}

	session, err := client.NewSession(model, effort, systemPrompt, enableExplictCaching, tools)
	if err != nil {
		fmt.Fprintf(os.Stderr, "bobo: create session: %s\n", err)
		os.Exit(1)
	}

	err = repl.REPL(func(prompt string) (string, error) { return client.Prompt(session, prompt) })
	if errors.Is(err, repl.ErrNotInteractive) {
		prompt, err := io.ReadAll(os.Stdin)
		if err != nil {
			fmt.Fprintf(os.Stderr, "bobo: read prompt from stdin: %s\n", err)
			os.Exit(1)
		}
		answer, err := client.Prompt(session, string(prompt))
		fmt.Println(answer)
		if err != nil {
			fmt.Fprintf(os.Stderr, "bobo: %s\n", err)
			os.Exit(1)
		}
	} else if err != nil {
		fmt.Fprintf(os.Stderr, "bobo: %s\n", err)
		os.Exit(1)
	}
}
