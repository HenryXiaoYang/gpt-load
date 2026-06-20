package keypool

import (
	"context"
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
	KeyValue            string `json:"key_value"`
	IsValid             bool   `json:"is_valid"`
	OpenAITier          string `json:"openai_tier,omitempty"`
	OpenAITierUpdated   bool   `json:"openai_tier_updated,omitempty"`
	OpenAIModel         string `json:"openai_model,omitempty"`
	OpenAIHost          string `json:"openai_host,omitempty"`
	OpenAIRequestsLimit string `json:"openai_requests_limit,omitempty"`
	OpenAITokensLimit   string `json:"openai_tokens_limit,omitempty"`
	OpenAITierReason    string `json:"openai_tier_reason,omitempty"`
	Error               string `json:"error,omitempty"`
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
	if validationResult.OpenAITierUpdated {
		key.OpenAITier = validationResult.OpenAITier
	}

	var errorMsg string
	if !isValid && validationErr != nil {
		errorMsg = validationErr.Error()
	}
	s.keypoolProvider.UpdateValidationResult(key, group, validationResult, errorMsg)

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

		validationResult, validationErr := s.ValidateSingleKey(&apiKey, group)

		results[i] = KeyTestResult{
			KeyValue:            kv,
			IsValid:             validationResult.IsValid,
			OpenAITier:          apiKey.OpenAITier,
			OpenAITierUpdated:   validationResult.OpenAITierUpdated,
			OpenAIModel:         validationResult.OpenAIModel,
			OpenAIHost:          validationResult.OpenAIHost,
			OpenAIRequestsLimit: validationResult.OpenAIRequestsLimit,
			OpenAITokensLimit:   validationResult.OpenAITokensLimit,
			OpenAITierReason:    validationResult.OpenAITierReason,
			Error:               "",
		}
		if validationErr != nil {
			results[i].Error = validationErr.Error()
		}
	}

	return results, nil
}
