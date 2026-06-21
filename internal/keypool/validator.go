package keypool

import (
	"context"
	"encoding/json"
	"fmt"
	"gpt-load/internal/channel"
	"gpt-load/internal/config"
	"gpt-load/internal/encryption"
	"gpt-load/internal/models"
	"time"

	"github.com/sirupsen/logrus"
	"go.uber.org/dig"
	"gorm.io/gorm"
)

const openAIImageGenerationValidationTimeoutSeconds = 300

// KeyTestResult holds the validation result for a single key.
type KeyTestResult struct {
	KeyValue          string `json:"key_value"`
	IsValid           bool   `json:"is_valid"`
	Tier              string `json:"tier,omitempty"`
	TierUpdated       bool   `json:"tier_updated,omitempty"`
	TierProvider      string `json:"tier_provider,omitempty"`
	TierReason        string `json:"tier_reason,omitempty"`
	TierModel         string `json:"tier_model,omitempty"`
	TierHost          string `json:"tier_host,omitempty"`
	RequestsLimit     string `json:"requests_limit,omitempty"`
	TokensLimit       string `json:"tokens_limit,omitempty"`
	InputTokensLimit  string `json:"input_tokens_limit,omitempty"`
	OutputTokensLimit string `json:"output_tokens_limit,omitempty"`
	Error             string `json:"error,omitempty"`
}

// KeyValidator provides methods to validate API keys.
type KeyValidator struct {
	DB              *gorm.DB
	channelFactory  *channel.Factory
	SettingsManager *config.SystemSettingsManager
	keypoolProvider *KeyProvider
	encryptionSvc   encryption.Service
}

type KeyValidatorParams struct {
	dig.In
	DB              *gorm.DB
	ChannelFactory  *channel.Factory
	SettingsManager *config.SystemSettingsManager
	KeypoolProvider *KeyProvider
	EncryptionSvc   encryption.Service
}

// NewKeyValidator creates a new KeyValidator.
func NewKeyValidator(params KeyValidatorParams) *KeyValidator {
	return &KeyValidator{
		DB:              params.DB,
		channelFactory:  params.ChannelFactory,
		SettingsManager: params.SettingsManager,
		keypoolProvider: params.KeypoolProvider,
		encryptionSvc:   params.EncryptionSvc,
	}
}

// PrepareValidationGroup builds a fresh group snapshot for validation requests.
// Validation should use the latest persisted config so proxy/header changes take
// effect immediately instead of waiting for async cache invalidation.
func (s *KeyValidator) PrepareValidationGroup(group *models.Group) (*models.Group, error) {
	if group == nil {
		return nil, fmt.Errorf("group is required")
	}

	preparedGroup := *group

	if group.ID != 0 {
		if err := s.DB.First(&preparedGroup, group.ID).Error; err != nil {
			return nil, fmt.Errorf("failed to load latest group config for group %d: %w", group.ID, err)
		}
	}

	preparedGroup.EffectiveConfig = s.SettingsManager.GetEffectiveConfig(preparedGroup.Config)

	if len(preparedGroup.HeaderRules) > 0 {
		if err := json.Unmarshal(preparedGroup.HeaderRules, &preparedGroup.HeaderRuleList); err != nil {
			logrus.WithError(err).WithField("group_id", preparedGroup.ID).Warn("Failed to parse header rules for validation, ignoring")
			preparedGroup.HeaderRuleList = []models.HeaderRule{}
		}
	} else {
		preparedGroup.HeaderRuleList = []models.HeaderRule{}
	}

	return &preparedGroup, nil
}

// ValidateSingleKey performs a validation check on a single API key.
func (s *KeyValidator) ValidateSingleKey(key *models.APIKey, group *models.Group) (channel.KeyValidationResult, error) {
	if group.EffectiveConfig.AppUrl == "" {
		group.EffectiveConfig = s.SettingsManager.GetEffectiveConfig(group.Config)
	}
	ctx, cancel := context.WithTimeout(context.Background(), keyValidationTimeout(group))
	defer cancel()

	ch, err := s.channelFactory.GetChannel(group)
	if err != nil {
		return channel.KeyValidationResult{}, fmt.Errorf("failed to get channel for group %s: %w", group.Name, err)
	}

	validationResult, validationErr := ch.ValidateKey(ctx, key, group)
	isValid := validationResult.IsValid
	if validationResult.TierUpdated && validationResult.Tier != "" {
		key.Tier = validationResult.Tier
	}
	if validationResult.OpenAITierUpdated && validationResult.OpenAITier != "" {
		key.OpenAITier = validationResult.OpenAITier
	}

	var errorMsg string
	if !isValid && validationErr != nil {
		errorMsg = validationErr.Error()
	}
	if err := s.keypoolProvider.ApplyValidationResult(key, group, validationResult, errorMsg); err != nil {
		logrus.WithFields(logrus.Fields{
			"error":    err,
			"key_id":   key.ID,
			"group_id": group.ID,
		}).Error("Failed to persist key validation result")
		if isValid {
			return validationResult, fmt.Errorf("failed to persist key validation result: %w", err)
		}
	}

	if !isValid {
		logrus.WithFields(logrus.Fields{
			"error":    validationErr,
			"key_id":   key.ID,
			"group_id": group.ID,
		}).Debug("Key validation failed")
		return validationResult, validationErr
	}

	logrus.WithFields(logrus.Fields{
		"key_id":   key.ID,
		"is_valid": isValid,
	}).Debug("Key validation successful")

	return validationResult, nil
}

func keyValidationTimeout(group *models.Group) time.Duration {
	timeoutSeconds := group.EffectiveConfig.KeyValidationTimeoutSeconds
	if group.ChannelType == "openai-image-generation" && timeoutSeconds < openAIImageGenerationValidationTimeoutSeconds {
		timeoutSeconds = openAIImageGenerationValidationTimeoutSeconds
	}
	return time.Duration(timeoutSeconds) * time.Second
}

// TestMultipleKeys performs a synchronous validation for a list of key values within a specific group.
func (s *KeyValidator) TestMultipleKeys(group *models.Group, keyValues []string) ([]KeyTestResult, error) {
	validationGroup, err := s.PrepareValidationGroup(group)
	if err != nil {
		return nil, err
	}

	results := make([]KeyTestResult, len(keyValues))

	// Generate hashes for all key values
	var keyHashes []string
	for _, keyValue := range keyValues {
		keyHash := s.encryptionSvc.Hash(keyValue)
		if keyHash == "" {
			continue
		}
		keyHashes = append(keyHashes, keyHash)
	}

	// Find which of the provided keys actually exist in the database for this group
	var existingKeys []models.APIKey
	if len(keyHashes) > 0 {
		if err := s.DB.Where("group_id = ? AND key_hash IN ?", group.ID, keyHashes).Find(&existingKeys).Error; err != nil {
			return nil, fmt.Errorf("failed to query keys from DB: %w", err)
		}
	}

	// Create a map of key_hash to APIKey for quick lookup
	existingKeyMap := make(map[string]models.APIKey)
	for _, k := range existingKeys {
		existingKeyMap[k.KeyHash] = k
	}

	for i, kv := range keyValues {
		keyHash := s.encryptionSvc.Hash(kv)
		apiKey, exists := existingKeyMap[keyHash]
		if !exists {
			results[i] = KeyTestResult{
				KeyValue: kv,
				IsValid:  false,
				Error:    "Key does not exist in this group or has been removed.",
			}
			continue
		}

		apiKey.KeyValue = kv

		validationResult, validationErr := s.ValidateSingleKey(&apiKey, validationGroup)

		results[i] = KeyTestResult{
			KeyValue:          kv,
			IsValid:           validationResult.IsValid,
			Tier:              validationResult.Tier,
			TierUpdated:       validationResult.TierUpdated,
			TierProvider:      validationResult.TierProvider,
			TierReason:        validationResult.TierReason,
			TierModel:         validationResult.TierModel,
			TierHost:          validationResult.TierHost,
			RequestsLimit:     validationResult.RequestsLimit,
			TokensLimit:       validationResult.TokensLimit,
			InputTokensLimit:  validationResult.InputTokensLimit,
			OutputTokensLimit: validationResult.OutputTokensLimit,
			Error:             "",
		}
		if validationErr != nil {
			results[i].Error = validationErr.Error()
		}
	}

	return results, nil
}
