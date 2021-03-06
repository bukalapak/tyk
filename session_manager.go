package main

import (
	"time"
)

// AccessSpecs define what URLS a user has access to an what methods are enabled
type AccessSpec struct {
	URL     string   `json:"url"`
	Methods []string `json:"methods"`
}

// AccessDefinition defines which versions of an API a key has access to
type AccessDefinition struct {
	APIName     string       `json:"api_name"`
	APIID       string       `json:"api_id"`
	Versions    []string     `json:"versions"`
	AllowedURLs []AccessSpec `bson:"allowed_urls"  json:"allowed_urls"` // mapped string MUST be a valid regex
}

// SessionState objects represent a current API session, mainly used for rate limiting.
type SessionState struct {
	LastCheck        int64                       `json:"last_check"`
	Allowance        float64                     `json:"allowance"`
	Rate             float64                     `json:"rate"`
	Per              float64                     `json:"per"`
	Expires          int64                       `json:"expires"`
	QuotaMax         int64                       `json:"quota_max"`
	QuotaRenews      int64                       `json:"quota_renews"`
	QuotaRemaining   int64                       `json:"quota_remaining"`
	QuotaRenewalRate int64                       `json:"quota_renewal_rate"`
	AccessRights     map[string]AccessDefinition `json:"access_rights"`
	OrgID            string                      `json:"org_id"`
	OauthClientID    string                      `json:"oauth_client_id"`
	BasicAuthData    struct {
		Password string `json:"password"`
	} `json:"basic_auth_data"`
	HMACEnabled   bool   `json:"hmac_enabled"`
	HmacSecret    string `json:"hmac_string"`
	IsInactive    bool   `json:"is_inactive"`
	ApplyPolicyID string `json:"apply_policy_id"`
	DataExpires   int64  `json:"data_expires"`
	Monitor       struct {
		TriggerLimits []float64 `json:"trigger_limits"`
	} `json:"monitor"`
	MetaData interface{} `json:"meta_data"`
	Tags     []string    `json:"tags"`
}

type PublicSessionState struct {
	Quota struct {
		QuotaMax       int64 `json:"quota_max"`
		QuotaRemaining int64 `json:"quota_remaining"`
		QuotaRenews    int64 `json:"quota_renews"`
	} `json:"quota"`
	RateLimit struct {
		Rate float64 `json:"requests"`
		Per  float64 `json:"per_unit"`
	} `json:"rate_limit"`
}

const (
	QuotaKeyPrefix     string = "quota-"
	RateLimitKeyPrefix string = "rate-limit-"
)

// SessionLimiter is the rate limiter for the API, use ForwardMessage() to
// check if a message should pass through or not
type SessionLimiter struct{}

// ForwardMessage will enforce rate limiting, returning false if session limits have been exceeded.
// Key values to manage rate are Rate and Per, e.g. Rate of 10 messages Per 10 seconds
func (l SessionLimiter) ForwardMessage(currentSession *SessionState, key string, store StorageHandler) (bool, int) {

	log.Debug("[RATELIMIT] Inbound raw key is: ", key)
	rateLimiterKey := RateLimitKeyPrefix + publicHash(key)
	log.Debug("[RATELIMIT] Rate limiter key is: ", rateLimiterKey)
	ratePerPeriodNow := store.SetRollingWindow(rateLimiterKey, int64(currentSession.Per), int64(currentSession.Per))

	log.Debug("Num Requests: ", ratePerPeriodNow)

	// Subtract by 1 because of the delayed add in the window
	if ratePerPeriodNow > (int(currentSession.Rate) - 1) {
		return false, 1
	}

	currentSession.Allowance--
	if !l.IsRedisQuotaExceeded(currentSession, key, store) {
		return true, 0
	}

	return false, 2

}

// ForwardMessageNaiveKey is the old redis-key ttl-based Rate limit, it could be gamed.
func (l SessionLimiter) ForwardMessageNaiveKey(currentSession *SessionState, key string, store StorageHandler) (bool, int) {

	log.Debug("[RATELIMIT] Inbound raw key is: ", key)
	rateLimiterKey := RateLimitKeyPrefix + publicHash(key)
	log.Debug("[RATELIMIT] Rate limiter key is: ", rateLimiterKey)
	ratePerPeriodNow := store.IncrememntWithExpire(rateLimiterKey, int64(currentSession.Per))

	if ratePerPeriodNow > (int64(currentSession.Rate)) {
		return false, 1
	}

	currentSession.Allowance--
	if !l.IsRedisQuotaExceeded(currentSession, key, store) {
		return true, 0
	}

	return false, 2

}

// IsQuotaExceeded will confirm if a session key has exceeded it's quota, if a quota has been exceeded,
// but the quata renewal time has passed, it will be refreshed.
func (l SessionLimiter) IsQuotaExceeded(currentSession *SessionState) bool {
	if currentSession.QuotaMax == -1 {
		// No quota set
		return false
	}

	if currentSession.QuotaRemaining == 0 {
		current := time.Now().Unix()
		if currentSession.QuotaRenews-current < 0 {
			// quota used up, but we're passed renewal time
			currentSession.QuotaRenews = current + currentSession.QuotaRenewalRate
			currentSession.QuotaRemaining = currentSession.QuotaMax
			return false
		}
		// quota used up
		return true
	}

	if currentSession.QuotaRemaining > 0 {
		currentSession.QuotaRemaining--
		return false
	}

	return true

}

func (l SessionLimiter) IsRedisQuotaExceeded(currentSession *SessionState, key string, store StorageHandler) bool {

	// Are they unlimited?
	if currentSession.QuotaMax == -1 {
		// No quota set
		return false
	}

	// Create the key
	log.Debug("[QUOTA] Inbound raw key is: ", key)
	rawKey := QuotaKeyPrefix + publicHash(key)
	log.Debug("[QUOTA] Quota limiter key is: ", rawKey)
	// INCR the key (If it equals 1 - set EXPIRE)
	qInt := store.IncrememntWithExpire(rawKey, currentSession.QuotaRenewalRate)

	// if the returned val is >= quota: block
	if (int64(qInt) - 1) >= currentSession.QuotaMax {
		return true
	}

	// If this is a new Quota period, ensure we let the end user know
	if int64(qInt) == 1 {
		current := time.Now().Unix()
		currentSession.QuotaRenews = current + currentSession.QuotaRenewalRate
	}

	// If not, pass and set the values of the session to quotamax - counter
	remaining := currentSession.QuotaMax - int64(qInt)

	if remaining < 0 {
		currentSession.QuotaRemaining = 0
	} else {
		currentSession.QuotaRemaining = remaining
	}
	return false
}

// createSampleSession is a debug function to create a mock session value
func createSampleSession() SessionState {
	var thisSession SessionState
	thisSession.Rate = 5.0
	thisSession.Allowance = thisSession.Rate
	thisSession.LastCheck = time.Now().Unix()
	thisSession.Per = 8.0
	thisSession.Expires = 0
	thisSession.QuotaRenewalRate = 300 // 5 minutes
	thisSession.QuotaRenews = time.Now().Unix()
	thisSession.QuotaRemaining = 10
	thisSession.QuotaMax = 10

	simpleDef := AccessDefinition{
		APIName:  "Test",
		APIID:    "1",
		Versions: []string{"Default"},
	}
	thisSession.AccessRights = map[string]AccessDefinition{}
	thisSession.AccessRights["1"] = simpleDef

	return thisSession
}
