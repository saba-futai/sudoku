/*
Copyright (C) 2026 by saba <contact me via issue>

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
GNU General Public License for more details.

You should have received a copy of the GNU General Public License
along with this program. If not, see <http://www.gnu.org/licenses/>.

In addition, no derivative work may use the name or imply association
with this application without prior consent.
*/
package config

const (
	DefaultPACRejectRuleURL      = "https://gcore.jsdelivr.net/gh/TG-Twilight/AWAvenue-Ads-Rule@main/Filters/AWAvenue-Ads-Rule-Clash.yaml"
	DefaultImplicitRejectRuleURL = "https://fastly.jsdelivr.net/gh/TG-Twilight/AWAvenue-Ads-Rule@main/Filters/AWAvenue-Ads-Rule-Clash.yaml"
)

// DefaultPACRuleURLs returns the recommended PAC rule sources.
//
// The list is intentionally small and CDN-friendly, and should cover both IPv4 and IPv6 CN traffic.
func DefaultPACRuleURLs() []string {
	return []string{
		"https://fastly.jsdelivr.net/gh/blackmatrix7/ios_rule_script@master/rule/Clash/ChinaMaxNoIP/ChinaMaxNoIP.list",
		"https://fastly.jsdelivr.net/gh/fernvenue/chn-cidr-list@master/ipv4.yaml",
		"https://fastly.jsdelivr.net/gh/fernvenue/chn-cidr-list@master/ipv6.yaml",
		RejectRulePrefix + DefaultPACRejectRuleURL,
	}
}
