package webhook

// Adapters is the single place that lists built-in HTTP channels. The
// composition root registers each key in the notifier registry; adding a new
// channel means adding one file with an Adapter and one line here.
func Adapters() map[string]Adapter {
	return map[string]Adapter{
		"wecom":    wecom{},
		"feishu":   feishu{},
		"dingtalk": dingtalk{},
		"telegram": telegram{},
		"slack":    slack{},
		"discord":  discord{},
		"teams":    teams{},
		"bark":     bark{},
		"ntfy":     ntfy{},
		"webhook":  generic{},
	}
}
