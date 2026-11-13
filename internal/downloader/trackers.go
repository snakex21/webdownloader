package downloader

import "strings"

// Hosts of ads, analytics, tag managers and consent banners. Skipping them is
// normal (an offline copy is better without them), so the result reports
// them separately from CDN files that the page needs to look right.
var trackerSuffixes = []string{
	"googletagmanager.com", "google-analytics.com", "analytics.google.com",
	"googlesyndication.com", "googleadservices.com", "doubleclick.net",
	"adservice.google.com", "googleoptimize.com", "pagead2.googlesyndication.com",
	"facebook.net", "connect.facebook.net",
	"optad360.io", "optad360.com", "gemius.pl", "hit.gemius.pl",
	"hotjar.com", "clarity.ms", "scorecardresearch.com", "quantserve.com",
	"quantcast.com", "criteo.com", "criteo.net", "taboola.com", "outbrain.com",
	"adnxs.com", "pubmatic.com", "rubiconproject.com", "amazon-adsystem.com",
	"adform.net", "smartadserver.com", "rtbhouse.com", "teads.tv",
	"doubleverify.com", "moatads.com", "cookiebot.com", "cookielaw.org",
	"onetrust.com", "consensu.org", "privacy-mgmt.com", "mc.yandex.ru",
	"stats.wp.com", "analytics.tiktok.com", "snap.licdn.com",
	"ads.linkedin.com", "bat.bing.com", "clarity.microsoft.com",
	"cloudflareinsights.com", "plausible.io", "matomo.cloud", "segment.io",
	"segment.com", "mixpanel.com", "newrelic.com", "nr-data.net",
	"adsrvr.org", "openx.net", "casalemedia.com", "3lift.com", "sharethrough.com",
	"yieldmo.com", "seedtag.com", "imasdk.googleapis.com",
}

// trackerLabels are first labels that mark ad, analytics or consent hosts
// (cmp.example.com, ads.example.com, …).
var trackerLabels = map[string]struct{}{
	"cmp": {}, "consent": {}, "ads": {}, "ad": {}, "adserver": {}, "adservice": {},
	"analytics": {}, "stats": {}, "tracking": {}, "tracker": {}, "pixel": {},
	"metrics": {}, "tag": {}, "tags": {}, "tagmanager": {}, "gtm": {},
}

// IsTrackerHost reports whether host serves ads, analytics or consent
// banners rather than content.
func IsTrackerHost(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, s := range trackerSuffixes {
		if host == s || strings.HasSuffix(host, "."+s) {
			return true
		}
	}
	first, rest, ok := strings.Cut(host, ".")
	if ok && strings.Contains(rest, ".") {
		if _, hit := trackerLabels[first]; hit {
			return true
		}
	}
	return false
}
