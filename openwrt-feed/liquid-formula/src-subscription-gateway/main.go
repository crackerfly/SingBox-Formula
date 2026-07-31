package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "liquid-formula-subscription-gateway: code=usage_error")
		return 2
	}
	switch args[0] {
	case "normalize":
		return runNormalize(args[1:], stdin, stdout, stderr)
	case "status":
		return runStatus(
			args[1:], stdout, stderr, defaultStatusDependencies(),
		)
	case "serve":
		return runServe(
			args[1:],
			stderr,
			newProductionServeDependencies(
				defaultSubscriptionEngineRuntime(),
			),
		)
	default:
		fmt.Fprintln(stderr, "liquid-formula-subscription-gateway: code=usage_error")
		return 2
	}
}

func runNormalize(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("normalize", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	inputPath := flags.String("input", "", "")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		fmt.Fprintln(stderr, "liquid-formula-subscription-gateway: code=usage_error")
		return 2
	}

	var reader io.Reader = stdin
	var input *os.File
	if *inputPath != "" {
		file, err := os.Open(*inputPath)
		if err != nil {
			fmt.Fprintln(stderr, "liquid-formula-subscription-gateway: code=input_read_failed")
			return 1
		}
		input = file
		defer input.Close()
		reader = input
	}

	raw, err := readBounded(reader)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	output, info, err := NormalizeDocument(raw)
	if err != nil {
		fmt.Fprintln(stderr, err)
		for _, warning := range info.Warnings {
			writeWarning(stderr, warning)
		}
		return 1
	}
	if _, err := io.Copy(stdout, bytes.NewReader(output)); err != nil {
		fmt.Fprintln(stderr, "liquid-formula-subscription-gateway: code=output_encode_failed")
		return 1
	}
	fmt.Fprintf(stderr, "normalized format=%s accepted=%d skipped=%d\n",
		info.Format, info.Accepted, info.Skipped)
	for _, warning := range info.Warnings {
		writeWarning(stderr, warning)
	}
	return 0
}

type serveDependencies struct {
	ReadFile  func(string) ([]byte, error)
	Listen    func(string, string) (net.Listener, error)
	NewEngine func(gatewayConfig) aggregateEngine
}

type requiredStringFlag struct {
	value string
	set   bool
}

func (value *requiredStringFlag) String() string {
	return value.value
}

func (value *requiredStringFlag) Set(raw string) error {
	if value.set {
		return errors.New("flag repeated")
	}
	value.value = raw
	value.set = true
	return nil
}

func runServe(args []string, stderr io.Writer, dependencies serveDependencies) int {
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var configPath requiredStringFlag
	var expectedDigest requiredStringFlag
	flags.Var(&configPath, "config", "")
	flags.Var(&expectedDigest, "expected-digest", "")
	if err := flags.Parse(args); err != nil ||
		flags.NArg() != 0 ||
		!configPath.set || configPath.value == "" ||
		!expectedDigest.set || !isLowerHexDigest(expectedDigest.value) {
		writeGatewayDiagnostic(stderr, "usage_error")
		return 2
	}

	config, err := readGatewayConfig(
		configPath.value,
		expectedDigest.value,
		dependencies.ReadFile,
	)
	if err != nil {
		var configError *gatewayConfigError
		if errors.As(err, &configError) {
			writeGatewayDiagnostic(stderr, configError.code)
		} else {
			writeGatewayDiagnostic(stderr, "config_invalid")
		}
		return 1
	}
	if dependencies.NewEngine == nil {
		writeGatewayDiagnostic(stderr, "config_invalid")
		return 1
	}
	engine := dependencies.NewEngine(config)
	if engine == nil {
		writeGatewayDiagnostic(stderr, "config_invalid")
		return 1
	}
	server, listener, err := openGatewayServer(
		config,
		engine,
		dependencies.Listen,
	)
	if err != nil {
		writeGatewayDiagnostic(stderr, "listen_failed")
		return 1
	}
	err = server.Serve(listener)
	if errors.Is(err, http.ErrServerClosed) {
		return 0
	}
	writeGatewayDiagnostic(stderr, "serve_failed")
	return 1
}

func writeGatewayDiagnostic(writer io.Writer, code string) {
	switch code {
	case "usage_error",
		"expected_digest_invalid",
		"config_read_failed",
		"config_digest_mismatch",
		"config_invalid",
		"listen_failed",
		"serve_failed":
	default:
		code = "config_invalid"
	}
	fmt.Fprintf(writer, "liquid-formula-subscription-gateway: code=%s\n", code)
}

func readBounded(reader io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(reader, MaxInputBytes+1))
	if err != nil {
		return nil, normalizeError("input_read_failed", FormatUnknown)
	}
	if len(data) > MaxInputBytes {
		return nil, normalizeError("input_too_large", FormatUnknown)
	}
	return data, nil
}

func writeWarning(writer io.Writer, warning Warning) {
	fmt.Fprintf(writer, "warning code=%s node_index=%d type=%s field=%s\n",
		safeWarningCode(warning.Code), warning.NodeIndex,
		safeType(warning.Type), safeField(warning.Field))
}
