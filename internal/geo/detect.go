// Package geo provides lightweight IP-based region detection.
package geo

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"
)

// DetectRegion calls ip-api.com and returns a region string like "US-California".
// Falls back to "auto" on any error so the gateway resolves from IP instead.
func DetectRegion() string {
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get("http://ip-api.com/json/?fields=status,countryCode,regionName")
	if err != nil {
		log.Printf("owlrun: geo detect: %v", err)
		return "auto"
	}
	defer resp.Body.Close()

	var result struct {
		Status      string `json:"status"`
		CountryCode string `json:"countryCode"`
		RegionName  string `json:"regionName"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil || result.Status != "success" {
		log.Printf("owlrun: geo detect: bad response")
		return "auto"
	}

	region := fmt.Sprintf("%s-%s", result.CountryCode, result.RegionName)
	log.Printf("owlrun: detected region %s", region)
	return region
}
