package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
)

// CrawlResult matches the JSONL output format we integrated into Crawley
type CrawlResult struct {
	URL    string `json:"url"`
	Status int    `json:"status"`
}

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: go run main.go <target-url>")
		os.Exit(1)
	}
	targetURL := os.Args[1]

	fmt.Printf("[*] Launching Crawley Audit on: %s\n", targetURL)
	fmt.Println("[*] Activating deep scanning (-js, -css, -brute) with real-time status checks...")
	fmt.Println("----------------------------------------------------------------------")

	// 1. Build the command to execute your Crawley binary
	// We pass -jsonl and -status to parse the metadata programmatically
	cmd := exec.Command("go", "run", "cmd/webora/main.go",
		"-status",
		"-jsonl",
		"-js",
		"-css",
		"-brute",
		"-depth", "2", // Cap depth so the example runs quickly
		targetURL,
	)

	// Capture stdout to read Crawley's real-time stream
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		log.Fatalf("[-] Failed to create stdout pipe: %v", err)
	}

	// Start running Crawley in the background
	if err := cmd.Start(); err != nil {
		log.Fatalf("[-] Failed to start Crawley execution: %v", err)
	}

	// Tracking maps to build our final audit report
	var brokenPages []CrawlResult
	var brokenAssets []CrawlResult
	var successCount int

	// 2. Stream and Parse Crawley's JSONL output line-by-line
	decoder := json.NewDecoder(stdout)
	for decoder.More() {
		var res CrawlResult
		if err := decoder.Decode(&res); err != nil {
			// Skip any non-JSON or malformed lines
			continue
		}

		// Check if the link returned a failure status code (4xx or 5xx)
		if res.Status >= 400 || res.Status == 0 {
			// Categorize whether it's a broken static asset or a broken web page
			if isStaticAsset(res.URL) {
				brokenAssets = append(brokenAssets, res)
				fmt.Printf("❌ BROKEN ASSET [%d]: %s\n", res.Status, res.URL)
			} else {
				brokenPages = append(brokenPages, res)
				fmt.Printf("❌ BROKEN PAGE  [%d]: %s\n", res.Status, res.URL)
			}
		} else {
			successCount++
		}
	}

	// Wait for Crawley to cleanly finish execution
	_ = cmd.Wait()

	// 3. Print the Final Audit Summary Report
	printSummary(successCount, brokenPages, brokenAssets)
}

// Helper function to identify if a URL points to a dynamic asset or static code layer
func isStaticAsset(url string) bool {
	u := strings.ToLower(url)
	return strings.HasSuffix(u, ".js") ||
		strings.HasSuffix(u, ".css") ||
		strings.HasSuffix(u, ".png") ||
		strings.HasSuffix(u, ".jpg") ||
		strings.HasSuffix(u, ".jpeg") ||
		strings.HasSuffix(u, ".svg")
}

// Helper function to render a clean terminal report dashboard
func printSummary(success int, pages []CrawlResult, assets []CrawlResult) {
	fmt.Println("\n----------------------------------------------------------------------")
	fmt.Println("📋 FINAL SITE AUDIT SUMMARY REPORT")
	fmt.Println("----------------------------------------------------------------------")
	fmt.Printf("✅ Healthy Endpoints Discovered: %d\n", success)
	fmt.Printf("❌ Total Broken Pages (4xx/5xx): %d\n", len(pages))
	fmt.Printf("🖼️  Total Broken Static Assets:   %d\n", len(assets))
	fmt.Println("----------------------------------------------------------------------")

	if len(pages) > 0 {
		fmt.Println("\n🚨 Action Required: Fix these broken pages:")
		for _, p := range pages {
			fmt.Printf("  • [%d] %s\n", p.Status, p.URL)
		}
	}

	if len(assets) > 0 {
		fmt.Println("\n🎨 Action Required: Fix these missing script/style dependencies:")
		for _, a := range assets {
			fmt.Printf("  • [%d] %s\n", a.Status, a.URL)
		}
	}

	if len(pages) == 0 && len(assets) == 0 {
		fmt.Println("\n🎉 Perfect Build! Zero dead links or broken files found.")
	}
	fmt.Println("----------------------------------------------------------------------")
}
