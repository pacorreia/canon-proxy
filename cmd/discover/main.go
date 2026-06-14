package main

import (
	"context"
	"flag"
	"fmt"
	"time"

	"github.com/pacorreia/canon-proxy/internal/canon"
)

func main() {
	passive  := flag.Bool("passive", false, "advertise via mDNS (like Camera Connect) and wait for camera to connect")
	phoneMac := flag.String("phone-mac", "", "WiFi MAC of the phone paired with the camera (required for -passive)")
	flag.Parse()

	if *passive {
		if *phoneMac == "" {
			fmt.Println("error: -passive requires -phone-mac (the WiFi MAC of the phone paired with the camera)")
			fmt.Println("  example: -phone-mac FC:9F:5E:D4:2C:8A")
			return
		}
		svcType := canon.ServiceTypeFromMAC(*phoneMac)
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		fmt.Printf("Passive mode: advertising %s via mDNS, waiting for camera (60s)...\n", svcType)
		cam, err := canon.AdvertiseAndWait(ctx, svcType)
		if err != nil {
			fmt.Printf("error: %v\n", err)
			return
		}
		fmt.Printf("Camera connected: IP=%-16s Port=%d  Name=%q\n", cam.IP, cam.Port, cam.Name)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fmt.Println("Discovering... (TCP scan, 30s)")
	cams, err := canon.DiscoverLAN(ctx, canon.DiscoverOptions{})
	if err != nil {
		fmt.Printf("error: %v\n", err)
		return
	}
	if len(cams) == 0 {
		fmt.Println("No cameras found.")
		return
	}
	for _, c := range cams {
		fmt.Printf("Found: IP=%-16s Port=%d  Name=%q\n", c.IP, c.Port, c.Name)
	}
}
