// Package trigger defines call events independently of Home Assistant.
package trigger

type Event struct {
	RouteID  string
	EntityID string
}
