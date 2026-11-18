package main

import "time"

// This file is the host application: plain Go with no knowledge of EVM.
// Plugins see it as package "shop".

// PointsPerDollar is the base earning rate.
const PointsPerDollar = 10

type Customer struct {
	Name    string
	Tier    string
	Points  int64
	Joined  time.Time
	History []string
}

// AddPoints credits the customer and records why.
func (c *Customer) AddPoints(points int64, reason string) {
	c.Points += points
	c.History = append(c.History, reason)
}

type Order struct {
	ID    string
	Cents int64
	Items []Item
}

type Item struct {
	SKU string
	Qty int
}

// TierThreshold returns the points a customer needs to hold a tier.
func TierThreshold(tier string) int64 {
	switch tier {
	case "gold":
		return 2000
	case "silver":
		return 500
	}
	return 0
}
