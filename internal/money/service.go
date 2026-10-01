package money

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image/png"
	"sort"
	"strconv"
	"strings"
	"time"

	"megaapp-back/internal/httpx/legacy"
	platformclock "megaapp-back/internal/platform/clock"
	"megaapp-back/internal/platform/idempotency"

	"github.com/disintegration/imaging"
)

type Service struct {
	repo        *Repository
	idempotency *idempotency.Store
	clock       platformclock.Clock
}

func NewService(repo *Repository, idempotencyStore *idempotency.Store) *Service {
	return NewServiceWithClock(repo, idempotencyStore, platformclock.NewRealClock())
}

func NewServiceWithClock(repo *Repository, idempotencyStore *idempotency.Store, appClock platformclock.Clock) *Service {
	if appClock == nil {
		appClock = platformclock.NewRealClock()
	}
	return &Service{repo: repo, idempotency: idempotencyStore, clock: appClock}
}

func (s *Service) GetSnapshot(ctx context.Context, userID int64) (Snapshot, error) {
	currencies, err := s.repo.ListCurrencies(ctx, userID)
	if err != nil {
		return Snapshot{}, err
	}
	categories, err := s.repo.ListCategories(ctx, userID)
	if err != nil {
		return Snapshot{}, err
	}
	organizations, err := s.repo.ListOrganizations(ctx, userID)
	if err != nil {
		return Snapshot{}, err
	}
	accounts, err := s.repo.ListAccounts(ctx, userID)
	if err != nil {
		return Snapshot{}, err
	}
	assets, err := s.repo.ListAssets(ctx, userID)
	if err != nil {
		return Snapshot{}, err
	}
	transactions, err := s.repo.ListTransactions(ctx, userID)
	if err != nil {
		return Snapshot{}, err
	}
	rateHistory, err := s.repo.ListRateHistory(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	investAssetTrades, err := s.repo.ListInvestAssetTrades(ctx, userID)
	if err != nil {
		return Snapshot{}, err
	}

	return Snapshot{
		Currencies:        currencies,
		Categories:        categories,
		Organizations:     organizations,
		Accounts:          accounts,
		Assets:            assets,
		InvestAssetTrades: investAssetTrades,
		Transactions:      transactions,
		RateHistory:       s.filterSnapshotRateHistory(rateHistory, currencies, investAssetTrades),
	}, nil
}

func (s *Service) GetOrganizations(ctx context.Context, userID int64) ([]Organization, error) {
	return s.repo.ListOrganizations(ctx, userID)
}

func (s *Service) CreateOrganization(ctx context.Context, userID int64, input OrganizationInput) (int64, error) {
	normalized, err := validateOrganizationInput(input)
	if err != nil {
		return 0, err
	}
	return s.repo.CreateOrganization(ctx, userID, normalized)
}

func (s *Service) UpdateOrganization(ctx context.Context, userID int64, organizationID int64, input OrganizationInput) error {
	existing, err := s.repo.GetOrganizationByID(ctx, userID, organizationID)
	if err != nil {
		return err
	}
	if existing == nil {
		return legacy.NewError(legacy.ErrorKindNotFound, "Organization not found")
	}

	normalized, err := validateOrganizationInput(input)
	if err != nil {
		return err
	}
	return s.repo.UpdateOrganization(ctx, userID, organizationID, normalized)
}

func (s *Service) DeleteOrganization(ctx context.Context, userID int64, organizationID int64) error {
	existing, err := s.repo.GetOrganizationByID(ctx, userID, organizationID)
	if err != nil {
		return err
	}
	if existing == nil {
		return legacy.NewError(legacy.ErrorKindNotFound, "Organization not found")
	}

	linkedAccountsCount, err := s.repo.CountAccountsByOrganization(ctx, userID, organizationID)
	if err != nil {
		return err
	}
	if linkedAccountsCount > 0 {
		return legacy.NewError(legacy.ErrorKindConflict, "Organization is linked to existing accounts")
	}

	return s.repo.DeleteOrganization(ctx, userID, organizationID)
}

func (s *Service) GetCurrencies(ctx context.Context, userID int64) ([]Currency, error) {
	return s.repo.ListCurrencies(ctx, userID)
}

func (s *Service) CreateCurrency(ctx context.Context, userID int64, input CurrencyInput) (int64, error) {
	normalized, err := validateCurrencyInput(input)
	if err != nil {
		return 0, err
	}
	return s.repo.CreateCurrency(ctx, userID, normalized)
}

func (s *Service) UpdateCurrency(ctx context.Context, userID int64, currencyID int64, input CurrencyInput) error {
	existing, err := s.repo.GetCurrencyByID(ctx, userID, currencyID)
	if err != nil {
		return err
	}
	if existing == nil {
		return legacy.NewError(legacy.ErrorKindNotFound, "Currency not found")
	}

	normalized, err := validateCurrencyInput(input)
	if err != nil {
		return err
	}
	return s.repo.UpdateCurrency(ctx, userID, currencyID, normalized)
}

func (s *Service) DeleteCurrency(ctx context.Context, userID int64, currencyID int64) error {
	existing, err := s.repo.GetCurrencyByID(ctx, userID, currencyID)
	if err != nil {
		return err
	}
	if existing == nil {
		return legacy.NewError(legacy.ErrorKindNotFound, "Currency not found")
	}

	linkedAccountsCount, err := s.repo.CountAccountsByCurrency(ctx, userID, currencyID)
	if err != nil {
		return err
	}
	if linkedAccountsCount > 0 {
		return legacy.NewError(legacy.ErrorKindConflict, "Currency is linked to existing accounts")
	}

	return s.repo.DeleteCurrency(ctx, userID, currencyID)
}

func (s *Service) GetCategories(ctx context.Context, userID int64) ([]Category, error) {
	return s.repo.ListCategories(ctx, userID)
}

func (s *Service) CreateCategory(ctx context.Context, userID int64, input CategoryInput) (int64, error) {
	normalized, err := validateCategoryInput(input)
	if err != nil {
		return 0, err
	}
	if normalized.ParentID != nil {
		parentCategory, err := s.repo.GetCategoryByID(ctx, userID, *normalized.ParentID)
		if err != nil {
			return 0, err
		}
		if parentCategory == nil {
			return 0, legacy.NewError(legacy.ErrorKindValidation, "Parent category not found")
		}
		if parentCategory.CategoryType != normalized.CategoryType {
			return 0, legacy.NewError(legacy.ErrorKindValidation, "Parent category type must match categoryType")
		}
	}
	return s.repo.CreateCategory(ctx, userID, normalized)
}

func (s *Service) UpdateCategory(ctx context.Context, userID int64, categoryID int64, input CategoryInput) error {
	existing, err := s.repo.GetCategoryByID(ctx, userID, categoryID)
	if err != nil {
		return err
	}
	if existing == nil {
		return legacy.NewError(legacy.ErrorKindNotFound, "Category not found")
	}

	normalized, err := validateCategoryInput(input)
	if err != nil {
		return err
	}
	if normalized.ParentID != nil && *normalized.ParentID == categoryID {
		return legacy.NewError(legacy.ErrorKindValidation, "Parent category cannot be the same as the category")
	}
	if normalized.ParentID != nil {
		parentCategory, err := s.repo.GetCategoryByID(ctx, userID, *normalized.ParentID)
		if err != nil {
			return err
		}
		if parentCategory == nil {
			return legacy.NewError(legacy.ErrorKindValidation, "Parent category not found")
		}
		if parentCategory.CategoryType != normalized.CategoryType {
			return legacy.NewError(legacy.ErrorKindValidation, "Parent category type must match categoryType")
		}
	}
	return s.repo.UpdateCategory(ctx, userID, categoryID, normalized)
}

func (s *Service) DeleteCategory(ctx context.Context, userID int64, categoryID int64) error {
	existing, err := s.repo.GetCategoryByID(ctx, userID, categoryID)
	if err != nil {
		return err
	}
	if existing == nil {
		return legacy.NewError(legacy.ErrorKindNotFound, "Category not found")
	}

	childCategoriesCount, err := s.repo.CountChildCategories(ctx, userID, categoryID)
	if err != nil {
		return err
	}
	if childCategoriesCount > 0 {
		return legacy.NewError(legacy.ErrorKindConflict, "Category has child categories")
	}

	linkedTransactionsCount, err := s.repo.CountTransactionsByCategory(ctx, userID, categoryID)
	if err != nil {
		return err
	}
	if linkedTransactionsCount > 0 {
		return legacy.NewError(legacy.ErrorKindConflict, "Category is linked to existing transactions")
	}

	return s.repo.DeleteCategory(ctx, userID, categoryID)
}

func (s *Service) GetAccounts(ctx context.Context, userID int64) ([]Account, error) {
	return s.repo.ListAccounts(ctx, userID)
}

func (s *Service) CreateAccount(ctx context.Context, userID int64, input AccountInput) (int64, error) {
	normalized, err := validateAccountInput(input)
	if err != nil {
		return 0, err
	}
	if err := s.ensureAccountReferences(ctx, userID, normalized); err != nil {
		return 0, err
	}
	return s.repo.CreateAccount(ctx, userID, normalized)
}

func (s *Service) UpdateAccount(ctx context.Context, userID int64, accountID int64, input AccountInput) error {
	existing, err := s.repo.GetAccountByID(ctx, userID, accountID)
	if err != nil {
		return err
	}
	if existing == nil {
		return legacy.NewError(legacy.ErrorKindNotFound, "Account not found")
	}

	normalized, err := validateAccountInput(input)
	if err != nil {
		return err
	}
	if err := s.ensureAccountReferences(ctx, userID, normalized); err != nil {
		return err
	}
	return s.repo.UpdateAccount(ctx, userID, accountID, normalized)
}

func (s *Service) DeleteAccount(ctx context.Context, userID int64, accountID int64) error {
	existing, err := s.repo.GetAccountByID(ctx, userID, accountID)
	if err != nil {
		return err
	}
	if existing == nil {
		return legacy.NewError(legacy.ErrorKindNotFound, "Account not found")
	}

	linkedTransactionsCount, err := s.repo.CountTransactionsByAccount(ctx, userID, accountID)
	if err != nil {
		return err
	}
	if linkedTransactionsCount > 0 {
		return legacy.NewError(legacy.ErrorKindConflict, "Account is linked to existing transactions")
	}

	linkedAssetsCount, err := s.repo.CountAssetsByAccount(ctx, userID, accountID)
	if err != nil {
		return err
	}
	if linkedAssetsCount > 0 {
		return legacy.NewError(legacy.ErrorKindConflict, "Account is linked to existing assets")
	}

	return s.repo.DeleteAccount(ctx, userID, accountID)
}

func (s *Service) GetAssets(ctx context.Context, userID int64) ([]Asset, error) {
	return s.repo.ListAssets(ctx, userID)
}

func (s *Service) CreateAsset(ctx context.Context, userID int64, input AssetInput) (int64, error) {
	normalized, err := validateAssetInput(input)
	if err != nil {
		return 0, err
	}
	if err := s.ensureAssetAccounts(ctx, userID, normalized.AccountIDs); err != nil {
		return 0, err
	}
	return s.repo.CreateAsset(ctx, userID, normalized)
}

func (s *Service) UpdateAsset(ctx context.Context, userID int64, assetID int64, input AssetInput) error {
	existing, err := s.repo.GetAssetByID(ctx, userID, assetID)
	if err != nil {
		return err
	}
	if existing == nil {
		return legacy.NewError(legacy.ErrorKindNotFound, "Asset not found")
	}

	normalized, err := validateAssetInput(input)
	if err != nil {
		return err
	}
	if err := s.ensureAssetAccounts(ctx, userID, normalized.AccountIDs); err != nil {
		return err
	}
	if !sameIDList(existing.AccountIDs, normalized.AccountIDs) {
		linkedAccountIDs, err := s.repo.ListLinkedTransactionAccountIDsByAsset(ctx, userID, assetID)
		if err != nil {
			return err
		}
		for _, linkedAccountID := range linkedAccountIDs {
			if !containsID(normalized.AccountIDs, linkedAccountID) {
				return legacy.NewError(legacy.ErrorKindConflict, "Asset accounts linked to existing transactions cannot be removed")
			}
		}
	}

	return s.repo.UpdateAsset(ctx, userID, assetID, normalized)
}

func (s *Service) DeleteAsset(ctx context.Context, userID int64, assetID int64) error {
	existing, err := s.repo.GetAssetByID(ctx, userID, assetID)
	if err != nil {
		return err
	}
	if existing == nil {
		return legacy.NewError(legacy.ErrorKindNotFound, "Asset not found")
	}

	linkedTransactionsCount, err := s.repo.CountTransactionsByAsset(ctx, userID, assetID)
	if err != nil {
		return err
	}
	if linkedTransactionsCount > 0 {
		return legacy.NewError(legacy.ErrorKindConflict, "Asset is linked to existing transactions")
	}

	return s.repo.DeleteAsset(ctx, userID, assetID)
}

func (s *Service) GetTransactions(ctx context.Context, userID int64) ([]Transaction, error) {
	return s.repo.ListTransactions(ctx, userID)
}

func (s *Service) GetInvestAssetTrades(ctx context.Context, userID int64) ([]InvestAssetTrade, error) {
	return s.repo.ListInvestAssetTrades(ctx, userID)
}

func (s *Service) GetRateHistory(ctx context.Context) ([]RateHistory, error) {
	return s.repo.ListRateHistory(ctx)
}

// peekIdempotency reports whether operationID has already been applied, without holding a
// transaction open across it. Checked first and unconditionally in every write method below —
// on a replay, nothing else (existence checks, cross-table validation) should run at all, since
// the entity a stale retry refers to may have legitimately been mutated or deleted since by an
// operation that already subsumes it. Cross-table validation reads (account/category/asset
// lookups) happen via r.db (read pool) between this peek and the real write's own transaction
// (write pool) — a concurrent write to one of those referenced rows in that window is a known,
// accepted race (same class as "stale retry over a newer edit", see plan 15), not something this
// function needs to close.
func (s *Service) peekIdempotency(ctx context.Context, userID int64, operationID string) (string, bool, error) {
	tx, err := s.idempotency.BeginTx(ctx)
	if err != nil {
		return "", false, err
	}
	cachedJSON, found, err := s.idempotency.Find(ctx, tx, userID, operationID)
	_ = tx.Rollback()
	return cachedJSON, found, err
}

func (s *Service) CreateTransaction(ctx context.Context, userID int64, operationID string, input TransactionInput) (CreateTransactionResult, bool, error) {
	cachedJSON, found, err := s.peekIdempotency(ctx, userID, operationID)
	if err != nil {
		return CreateTransactionResult{}, false, err
	}
	if found {
		var cached CreateTransactionResult
		if err := json.Unmarshal([]byte(cachedJSON), &cached); err != nil {
			return CreateTransactionResult{}, false, fmt.Errorf("decode cached transaction create: %w", err)
		}
		return cached, false, nil
	}

	normalized, err := validateTransactionInput(input)
	if err != nil {
		return CreateTransactionResult{}, false, err
	}
	if err := s.ensureTransactionAccount(ctx, userID, normalized.AccountID); err != nil {
		return CreateTransactionResult{}, false, err
	}
	isTransfer := normalized.Kind == TransactionKindTransfer
	if isTransfer {
		if err := s.ensureTransactionAccount(ctx, userID, *normalized.TwinAccountID); err != nil {
			return CreateTransactionResult{}, false, err
		}
	} else if isInvestTransactionKind(normalized.Kind) {
		normalized, err = s.normalizeInvestTransaction(ctx, userID, normalized)
		if err != nil {
			return CreateTransactionResult{}, false, err
		}
	} else {
		if err := s.ensureTransactionCategory(ctx, userID, normalized.CategoryID, normalized.Kind); err != nil {
			return CreateTransactionResult{}, false, err
		}
	}

	tx, err := s.idempotency.BeginTx(ctx)
	if err != nil {
		return CreateTransactionResult{}, false, err
	}

	var result CreateTransactionResult
	if isTransfer {
		result, err = s.repo.CreateTransferPair(ctx, tx, userID, normalized)
	} else {
		var createdID int64
		createdID, err = s.repo.CreateTransaction(ctx, tx, userID, normalized)
		result = CreateTransactionResult{ID: createdID, Version: 1}
	}
	if err != nil {
		_ = tx.Rollback()
		return CreateTransactionResult{}, false, err
	}

	resultJSON, err := json.Marshal(result)
	if err != nil {
		_ = tx.Rollback()
		return CreateTransactionResult{}, false, err
	}
	if err := s.idempotency.Record(ctx, tx, userID, operationID, string(resultJSON)); err != nil {
		_ = tx.Rollback()
		return CreateTransactionResult{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return CreateTransactionResult{}, false, fmt.Errorf("commit create transaction: %w", err)
	}
	return result, true, nil
}

func (s *Service) UpdateTransaction(ctx context.Context, userID int64, operationID string, transactionID int64, input TransactionInput) (newVersion int64, applied bool, err error) {
	cachedJSON, found, err := s.peekIdempotency(ctx, userID, operationID)
	if err != nil {
		return 0, false, err
	}
	if found {
		var cached struct {
			Version int64 `json:"version"`
		}
		if err := json.Unmarshal([]byte(cachedJSON), &cached); err != nil {
			return 0, false, fmt.Errorf("decode cached transaction update: %w", err)
		}
		return cached.Version, false, nil
	}

	existing, err := s.repo.GetTransactionByID(ctx, s.repo.db, userID, transactionID)
	if err != nil {
		return 0, false, err
	}
	if existing == nil {
		return 0, false, legacy.NewError(legacy.ErrorKindNotFound, "Transaction not found")
	}

	normalized, err := validateTransactionInput(input)
	if err != nil {
		return 0, false, err
	}
	if normalized.Kind != existing.Kind {
		return 0, false, legacy.NewError(legacy.ErrorKindValidation, "Transaction kind cannot be changed")
	}
	if normalized.AccountID != existing.AccountID {
		return 0, false, legacy.NewError(legacy.ErrorKindValidation, "Account cannot be changed")
	}

	isTransfer := existing.Kind == TransactionKindTransfer
	var twinTransaction *Transaction
	if isTransfer {
		if normalized.TwinAccountID == nil || normalized.TwinAmount == nil {
			return 0, false, missingFieldsError("twinAccountId", "twinAmount")
		}
		if existing.TwinID == nil {
			return 0, false, legacy.NewError(legacy.ErrorKindValidation, "Transfer pair is missing")
		}
		twinTransaction, err = s.repo.GetTransactionByID(ctx, s.repo.db, userID, *existing.TwinID)
		if err != nil {
			return 0, false, err
		}
		if twinTransaction == nil {
			return 0, false, legacy.NewError(legacy.ErrorKindValidation, "Transfer pair is missing")
		}
		if *normalized.TwinAccountID != twinTransaction.AccountID {
			return 0, false, legacy.NewError(legacy.ErrorKindValidation, "Account cannot be changed")
		}
	} else if isInvestTransactionKind(existing.Kind) {
		normalized, err = s.normalizeExistingInvestTransaction(ctx, userID, *existing, normalized)
		if err != nil {
			return 0, false, err
		}
	} else {
		if err := s.ensureTransactionCategory(ctx, userID, normalized.CategoryID, normalized.Kind); err != nil {
			return 0, false, err
		}
	}

	tx, err := s.idempotency.BeginTx(ctx)
	if err != nil {
		return 0, false, err
	}

	newVersion = existing.Version + 1
	if isTransfer {
		err = s.repo.UpdateTransferPair(ctx, tx, userID, existing.ID, twinTransaction.ID, normalized, newVersion, twinTransaction.Version+1)
	} else {
		err = s.repo.UpdateTransaction(ctx, tx, userID, transactionID, normalized, newVersion)
	}
	if err != nil {
		_ = tx.Rollback()
		return 0, false, err
	}

	resultJSON, err := json.Marshal(struct {
		Version int64 `json:"version"`
	}{newVersion})
	if err != nil {
		_ = tx.Rollback()
		return 0, false, err
	}
	if err := s.idempotency.Record(ctx, tx, userID, operationID, string(resultJSON)); err != nil {
		_ = tx.Rollback()
		return 0, false, err
	}
	if err := tx.Commit(); err != nil {
		return 0, false, fmt.Errorf("commit update transaction: %w", err)
	}
	return newVersion, true, nil
}

func (s *Service) DeleteTransaction(ctx context.Context, userID int64, operationID string, transactionID int64) (applied bool, err error) {
	_, found, err := s.peekIdempotency(ctx, userID, operationID)
	if err != nil {
		return false, err
	}
	if found {
		return false, nil
	}

	existing, err := s.repo.GetTransactionByID(ctx, s.repo.db, userID, transactionID)
	if err != nil {
		return false, err
	}
	if existing == nil {
		return false, legacy.NewError(legacy.ErrorKindNotFound, "Transaction not found")
	}

	tx, err := s.idempotency.BeginTx(ctx)
	if err != nil {
		return false, err
	}

	if err := s.repo.DeleteTransaction(ctx, tx, userID, transactionID); err != nil {
		_ = tx.Rollback()
		return false, err
	}
	if err := s.idempotency.Record(ctx, tx, userID, operationID, "{}"); err != nil {
		_ = tx.Rollback()
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit delete transaction: %w", err)
	}
	return true, nil
}

func (s *Service) ensureAccountReferences(ctx context.Context, userID int64, input AccountInput) error {
	currency, err := s.repo.GetCurrencyByID(ctx, userID, input.CurrencyID)
	if err != nil {
		return err
	}
	if currency == nil {
		return legacy.NewError(legacy.ErrorKindValidation, "Currency not found")
	}
	if input.OrganizationID == nil {
		return nil
	}
	organization, err := s.repo.GetOrganizationByID(ctx, userID, *input.OrganizationID)
	if err != nil {
		return err
	}
	if organization == nil {
		return legacy.NewError(legacy.ErrorKindValidation, "Organization not found")
	}
	return nil
}

func (s *Service) ensureAssetAccounts(ctx context.Context, userID int64, accountIDs []int64) error {
	for _, accountID := range accountIDs {
		account, err := s.repo.GetAccountByID(ctx, userID, accountID)
		if err != nil {
			return err
		}
		if account == nil {
			return legacy.NewError(legacy.ErrorKindValidation, fmt.Sprintf("Account not found: %d", accountID))
		}
		if account.Kind != AccountKindBrokerage && account.Kind != AccountKindCrypto {
			return legacy.NewError(legacy.ErrorKindValidation, fmt.Sprintf("Asset account must be brokerage or crypto: %d", accountID))
		}
	}
	return nil
}

func (s *Service) ensureTransactionAccount(ctx context.Context, userID int64, accountID int64) error {
	account, err := s.repo.GetAccountByID(ctx, userID, accountID)
	if err != nil {
		return err
	}
	if account == nil {
		return legacy.NewError(legacy.ErrorKindValidation, "Account not found")
	}
	return nil
}

func (s *Service) ensureTransactionCategory(ctx context.Context, userID int64, categoryID *int64, kind TransactionKind) error {
	if categoryID == nil {
		return nil
	}
	category, err := s.repo.GetCategoryByID(ctx, userID, *categoryID)
	if err != nil {
		return err
	}
	if category == nil {
		return legacy.NewError(legacy.ErrorKindValidation, "Category not found")
	}
	if category.CategoryType != CategoryType(kind) {
		return legacy.NewError(legacy.ErrorKindValidation, "Category type must match transaction kind")
	}
	if category.ParentID == nil {
		return nil
	}
	parentCategory, err := s.repo.GetCategoryByID(ctx, userID, *category.ParentID)
	if err != nil {
		return err
	}
	if parentCategory == nil || parentCategory.CategoryType != CategoryType(kind) {
		return legacy.NewError(legacy.ErrorKindValidation, "Category parent must match transaction kind")
	}
	return nil
}

func (s *Service) normalizeInvestTransaction(ctx context.Context, userID int64, input TransactionInput) (TransactionInput, error) {
	account, err := s.repo.GetAccountByID(ctx, userID, input.AccountID)
	if err != nil {
		return TransactionInput{}, err
	}
	if account == nil {
		return TransactionInput{}, legacy.NewError(legacy.ErrorKindValidation, "Account not found")
	}
	if account.Kind != AccountKindBrokerage && account.Kind != AccountKindCrypto {
		return TransactionInput{}, legacy.NewError(legacy.ErrorKindValidation, "Invest transaction requires brokerage or crypto account")
	}
	if input.CategoryID != nil {
		return TransactionInput{}, legacy.NewError(legacy.ErrorKindValidation, "Category is not allowed for invest transactions")
	}

	details := normalizeDetailsJSON(input.DetailsJSON)
	if details == nil {
		return TransactionInput{}, legacy.NewError(legacy.ErrorKindValidation, "detailsJSON is required for invest transactions")
	}
	assetID, ok := positiveIDFromValue(details["assetId"])
	if !ok {
		return TransactionInput{}, legacy.NewError(legacy.ErrorKindValidation, "detailsJSON.assetId must be a positive number")
	}

	asset, err := s.repo.GetAssetByID(ctx, userID, assetID)
	if err != nil {
		return TransactionInput{}, err
	}
	if asset == nil {
		return TransactionInput{}, legacy.NewError(legacy.ErrorKindValidation, "Asset not found")
	}
	if !containsID(asset.AccountIDs, input.AccountID) {
		return TransactionInput{}, legacy.NewError(legacy.ErrorKindValidation, "Asset does not belong to selected account")
	}

	amount, normalizedDetails, err := validateInvestPayload(input.Kind, input.Amount, details, asset.Type)
	if err != nil {
		return TransactionInput{}, err
	}
	storedDetailsJSON, err := marshalNormalizedJSON(normalizedDetails)
	if err != nil {
		return TransactionInput{}, err
	}

	input.Amount = amount
	input.CategoryID = nil
	input.IsGift = false
	input.StoredDetailsJSON = storedDetailsJSON
	return input, nil
}

func (s *Service) normalizeExistingInvestTransaction(ctx context.Context, userID int64, existing Transaction, input TransactionInput) (TransactionInput, error) {
	nextDetails := normalizeDetailsJSON(input.DetailsJSON)
	if nextDetails == nil {
		return TransactionInput{}, legacy.NewError(legacy.ErrorKindValidation, "detailsJSON is required for invest transactions")
	}
	nextAssetID, ok := positiveIDFromValue(nextDetails["assetId"])
	if !ok {
		return TransactionInput{}, legacy.NewError(legacy.ErrorKindValidation, "detailsJSON.assetId must be a positive number")
	}

	existingDetails := normalizeDetailsJSON(existing.DetailsJSON)
	prevAssetID, ok := positiveIDFromValue(existingDetails["assetId"])
	if !ok {
		return TransactionInput{}, legacy.NewError(legacy.ErrorKindValidation, "Existing invest transaction has invalid asset binding")
	}
	if nextAssetID != prevAssetID {
		return TransactionInput{}, legacy.NewError(legacy.ErrorKindValidation, "Asset cannot be changed")
	}
	return s.normalizeInvestTransaction(ctx, userID, input)
}

func (s *Service) filterSnapshotRateHistory(rateHistory []RateHistory, currencies []Currency, investAssetTrades []InvestAssetTrade) []SnapshotRateHistory {
	currencyTickers := make(map[string]struct{}, len(currencies))
	for _, currency := range currencies {
		currencyTickers[currency.Ticker] = struct{}{}
	}
	eomHeld := s.computeEOMHeldTickers(investAssetTrades)

	result := make([]SnapshotRateHistory, 0, len(rateHistory))
	for _, record := range rateHistory {
		ratesJSON, ok := parseRatesJSON(record.RatesJSON)
		if !ok {
			continue
		}
		if _, _, _, ok := parseISODateParts(record.DateISO); !ok {
			continue
		}
		allowedTickers := cloneTickerSet(currencyTickers)
		for ticker := range eomHeld[record.DateISO[:7]] {
			allowedTickers[ticker] = struct{}{}
		}

		filtered := make(map[string]float64)
		for ticker, rate := range ratesJSON {
			if _, exists := allowedTickers[ticker]; exists {
				filtered[ticker] = rate
			}
		}
		if len(filtered) == 0 {
			continue
		}

		result = append(result, SnapshotRateHistory{ID: record.ID, DateISO: record.DateISO, RatesJSON: filtered})
	}
	return result
}

func (s *Service) computeEOMHeldTickers(investAssetTrades []InvestAssetTrade) map[string]map[string]struct{} {
	type tradeEvent struct {
		ID      int64
		DateISO string
		Ticker  string
		Qty     float64
	}

	events := make([]tradeEvent, 0, len(investAssetTrades))
	for _, trade := range investAssetTrades {
		if trade.AssetTicker == nil || *trade.AssetTicker == "" {
			continue
		}
		details := normalizeDetailsJSON(trade.DetailsJSON)
		quantity, ok := finiteNumber(details["quantity"])
		if !ok || quantity <= 0 {
			continue
		}
		delta := quantity
		if trade.Kind == TransactionKindInvestSell {
			delta = -delta
		}
		events = append(events, tradeEvent{ID: trade.ID, DateISO: trade.DateISO, Ticker: *trade.AssetTicker, Qty: delta})
	}
	if len(events) == 0 {
		return map[string]map[string]struct{}{}
	}

	sort.Slice(events, func(i int, j int) bool {
		if events[i].DateISO == events[j].DateISO {
			return events[i].ID < events[j].ID
		}
		return events[i].DateISO < events[j].DateISO
	})

	firstYear, firstMonth, _, ok := parseISODateParts(events[0].DateISO)
	if !ok {
		return map[string]map[string]struct{}{}
	}
	now := s.clock.Now()
	lastYear := now.Year()
	lastMonth := int(now.Month())

	result := make(map[string]map[string]struct{})
	cumulativeQty := make(map[string]float64)
	tradeIndex := 0
	for year, month := firstYear, firstMonth; year < lastYear || (year == lastYear && month <= lastMonth); {
		eomISO := formatISODate(year, month, daysInMonth(year, month))
		monthKey := formatYearMonth(year, month)
		for tradeIndex < len(events) && events[tradeIndex].DateISO <= eomISO {
			event := events[tradeIndex]
			cumulativeQty[event.Ticker] += event.Qty
			tradeIndex++
		}

		held := make(map[string]struct{})
		for ticker, quantity := range cumulativeQty {
			if quantity > 1e-9 {
				held[ticker] = struct{}{}
			}
		}
		result[monthKey] = held

		month++
		if month > 12 {
			month = 1
			year++
		}
	}
	return result
}

func validateOrganizationInput(input OrganizationInput) (OrganizationInput, error) {
	title := strings.TrimSpace(input.Title)
	if title == "" {
		return OrganizationInput{}, missingFieldsError("title")
	}

	logo, err := normalizeLogoBase64(input.LogoBase64)
	if err != nil {
		return OrganizationInput{}, err
	}

	return OrganizationInput{Title: title, LogoBase64: logo}, nil
}

func validateCurrencyInput(input CurrencyInput) (CurrencyInput, error) {
	title := strings.TrimSpace(input.Title)
	ticker := strings.TrimSpace(input.Ticker)
	symbol := strings.TrimSpace(input.Symbol)

	var missing []string
	if title == "" {
		missing = append(missing, "title")
	}
	if ticker == "" {
		missing = append(missing, "ticker")
	}
	if symbol == "" {
		missing = append(missing, "symbol")
	}
	if len(missing) > 0 {
		return CurrencyInput{}, missingFieldsError(missing...)
	}
	if input.SymbolPosEnum != SymbolPositionBefore && input.SymbolPosEnum != SymbolPositionAfter {
		return CurrencyInput{}, legacy.NewError(legacy.ErrorKindValidation, `symbolPosEnum must be either "before" or "after"`)
	}

	return CurrencyInput{
		Title:         title,
		Ticker:        ticker,
		Symbol:        symbol,
		SymbolPosEnum: input.SymbolPosEnum,
		Whitespace:    input.Whitespace,
	}, nil
}

func validateCategoryInput(input CategoryInput) (CategoryInput, error) {
	name := strings.TrimSpace(input.Name)

	var missing []string
	if name == "" {
		missing = append(missing, "name")
	}
	if input.CategoryType == "" {
		missing = append(missing, "categoryType")
	}
	if len(missing) > 0 {
		return CategoryInput{}, missingFieldsError(missing...)
	}
	if input.CategoryType != CategoryTypeIncome && input.CategoryType != CategoryTypeExpense {
		return CategoryInput{}, legacy.NewError(legacy.ErrorKindValidation, "categoryType must be one of: income, expense")
	}
	if input.ParentID != nil && *input.ParentID <= 0 {
		return CategoryInput{}, legacy.NewError(legacy.ErrorKindValidation, "Parent category not found")
	}

	return CategoryInput{Name: name, ParentID: input.ParentID, CategoryType: input.CategoryType}, nil
}

func validateAccountInput(input AccountInput) (AccountInput, error) {
	title := strings.TrimSpace(input.Title)

	var missing []string
	if title == "" {
		missing = append(missing, "title")
	}
	if input.CurrencyID <= 0 {
		missing = append(missing, "currencyId")
	}
	if input.Kind == "" {
		missing = append(missing, "kind")
	}
	if len(missing) > 0 {
		return AccountInput{}, missingFieldsError(missing...)
	}
	if !isValidAccountKind(input.Kind) {
		return AccountInput{}, legacy.NewError(legacy.ErrorKindValidation, "kind must be one of: cash, card, checking, deposit, brokerage, crypto")
	}
	if input.OrganizationID != nil && *input.OrganizationID <= 0 {
		return AccountInput{}, legacy.NewError(legacy.ErrorKindValidation, "Organization not found")
	}

	return AccountInput{
		Title:          title,
		CurrencyID:     input.CurrencyID,
		IsInvest:       input.IsInvest,
		IsArchived:     input.IsArchived,
		Kind:           input.Kind,
		OrganizationID: input.OrganizationID,
	}, nil
}

func validateAssetInput(input AssetInput) (AssetInput, error) {
	title := strings.TrimSpace(input.Title)
	ticker := strings.TrimSpace(input.Ticker)

	var missing []string
	if title == "" {
		missing = append(missing, "title")
	}
	if ticker == "" {
		missing = append(missing, "ticker")
	}
	if input.Type == "" {
		missing = append(missing, "type")
	}
	if input.AccountIDs == nil {
		missing = append(missing, "accountIds")
	}
	if len(missing) > 0 {
		return AssetInput{}, missingFieldsError(missing...)
	}
	if !isValidAssetType(input.Type) {
		return AssetInput{}, legacy.NewError(legacy.ErrorKindValidation, "type must be one of: stock, bond, crypto")
	}

	normalizedAccountIDs := normalizeAccountIDs(input.AccountIDs)
	if len(normalizedAccountIDs) == 0 {
		return AssetInput{}, legacy.NewError(legacy.ErrorKindValidation, "accountIds must contain at least one brokerage account id")
	}

	suspendedSince, err := normalizeISODate(input.SuspendedSince, "suspendedSince")
	if err != nil {
		return AssetInput{}, err
	}
	suspendedUntil, err := normalizeISODate(input.SuspendedUntil, "suspendedUntil")
	if err != nil {
		return AssetInput{}, err
	}
	if suspendedUntil != nil && suspendedSince == nil {
		return AssetInput{}, legacy.NewError(legacy.ErrorKindValidation, "suspendedUntil requires suspendedSince to be set")
	}

	return AssetInput{
		Title:          title,
		Ticker:         ticker,
		Type:           input.Type,
		AccountIDs:     normalizedAccountIDs,
		SuspendedSince: suspendedSince,
		SuspendedUntil: suspendedUntil,
	}, nil
}

func validateTransactionInput(input TransactionInput) (TransactionInput, error) {
	dateISO := strings.TrimSpace(input.DateISO)

	var missing []string
	if dateISO == "" {
		missing = append(missing, "dateISO")
	}
	if input.AccountID <= 0 {
		missing = append(missing, "accountId")
	}
	if input.Kind == "" {
		missing = append(missing, "kind")
	}
	if len(missing) > 0 {
		return TransactionInput{}, missingFieldsError(missing...)
	}
	if !isSupportedTransactionKind(input.Kind) {
		return TransactionInput{}, legacy.NewError(legacy.ErrorKindValidation, "kind must be one of: income, expense, transfer, invest_buy, invest_sell, invest_dividend")
	}

	normalized := TransactionInput{
		DateISO:     dateISO,
		AccountID:   input.AccountID,
		Amount:      input.Amount,
		CategoryID:  normalizeOptionalID(input.CategoryID),
		Kind:        input.Kind,
		IsGift:      input.IsGift,
		Notes:       normalizeOptionalString(input.Notes),
		DetailsJSON: input.DetailsJSON,
	}

	if input.Kind == TransactionKindTransfer {
		if input.Amount <= 0 {
			return TransactionInput{}, legacy.NewError(legacy.ErrorKindValidation, "amount must be greater than 0")
		}
		if input.TwinAccountID == nil || *input.TwinAccountID <= 0 {
			return TransactionInput{}, missingFieldsError("twinAccountId")
		}
		if input.TwinAmount == nil {
			return TransactionInput{}, missingFieldsError("twinAmount")
		}
		if *input.TwinAmount <= 0 {
			return TransactionInput{}, legacy.NewError(legacy.ErrorKindValidation, "amount must be greater than 0")
		}
		if input.AccountID == *input.TwinAccountID {
			return TransactionInput{}, legacy.NewError(legacy.ErrorKindValidation, "Transfer accounts must be different")
		}
		if normalized.CategoryID != nil {
			return TransactionInput{}, legacy.NewError(legacy.ErrorKindValidation, "Category is not allowed for transfer")
		}
		twinAccountID := *input.TwinAccountID
		twinAmount := *input.TwinAmount
		normalized.TwinAccountID = &twinAccountID
		normalized.TwinAmount = &twinAmount
		normalized.CategoryID = nil
		normalized.IsGift = false
		return normalized, nil
	}

	if !isInvestTransactionKind(input.Kind) && input.Amount < 0 {
		return TransactionInput{}, legacy.NewError(legacy.ErrorKindValidation, "amount must be greater than or equal to 0")
	}
	return normalized, nil
}

func isValidAccountKind(kind AccountKind) bool {
	switch kind {
	case AccountKindCash, AccountKindCard, AccountKindChecking, AccountKindDeposit, AccountKindBrokerage, AccountKindCrypto:
		return true
	default:
		return false
	}
}

func isValidAssetType(assetType AssetType) bool {
	switch assetType {
	case AssetTypeStock, AssetTypeBond, AssetTypeCrypto:
		return true
	default:
		return false
	}
}

func isSupportedTransactionKind(kind TransactionKind) bool {
	switch kind {
	case TransactionKindIncome, TransactionKindExpense, TransactionKindTransfer, TransactionKindInvestBuy, TransactionKindInvestSell, TransactionKindInvestDividend:
		return true
	default:
		return false
	}
}

func isInvestTransactionKind(kind TransactionKind) bool {
	switch kind {
	case TransactionKindInvestBuy, TransactionKindInvestSell, TransactionKindInvestDividend:
		return true
	default:
		return false
	}
}

func missingFieldsError(fields ...string) error {
	return legacy.NewError(legacy.ErrorKindValidation, fmt.Sprintf("Missing required fields: %s", strings.Join(fields, ", ")))
}

func normalizeOptionalID(value *int64) *int64 {
	if value == nil || *value <= 0 {
		return nil
	}
	result := *value
	return &result
}

func normalizeOptionalString(value *string) *string {
	if value == nil || *value == "" {
		return nil
	}
	result := *value
	return &result
}

func normalizeDetailsJSON(value any) map[string]any {
	if value == nil {
		return nil
	}
	switch current := value.(type) {
	case map[string]any:
		return current
	case *string:
		if current == nil || *current == "" {
			return nil
		}
		var result map[string]any
		if err := json.Unmarshal([]byte(*current), &result); err != nil {
			return nil
		}
		return result
	case string:
		if strings.TrimSpace(current) == "" {
			return nil
		}
		var result map[string]any
		if err := json.Unmarshal([]byte(current), &result); err != nil {
			return nil
		}
		return result
	}

	encoded, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	var result map[string]any
	if err := json.Unmarshal(encoded, &result); err != nil {
		return nil
	}
	return result
}

func positiveIDFromValue(value any) (int64, bool) {
	number, ok := finiteNumber(value)
	if !ok {
		return 0, false
	}
	id := int64(number)
	if number != float64(id) || id <= 0 {
		return 0, false
	}
	return id, true
}

func finiteNumber(value any) (float64, bool) {
	switch current := value.(type) {
	case float64:
		return current, true
	case float32:
		return float64(current), true
	case int:
		return float64(current), true
	case int64:
		return float64(current), true
	case json.Number:
		parsed, err := current.Float64()
		if err != nil {
			return 0, false
		}
		return parsed, true
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(current), 64)
		if err != nil {
			return 0, false
		}
		return parsed, true
	default:
		return 0, false
	}
}

func parseRatesJSON(value string) (map[string]float64, bool) {
	var result map[string]float64
	if err := json.Unmarshal([]byte(value), &result); err != nil {
		return nil, false
	}
	if result == nil {
		return nil, false
	}
	return result, true
}

func parseISODateParts(value string) (int, int, int, bool) {
	if len(value) != len("2006-01-02") {
		return 0, 0, 0, false
	}
	year, err := strconv.Atoi(value[0:4])
	if err != nil {
		return 0, 0, 0, false
	}
	month, err := strconv.Atoi(value[5:7])
	if err != nil || month < 1 || month > 12 {
		return 0, 0, 0, false
	}
	day, err := strconv.Atoi(value[8:10])
	if err != nil || day < 1 || day > 31 {
		return 0, 0, 0, false
	}
	return year, month, day, true
}

func cloneTickerSet(values map[string]struct{}) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for value := range values {
		result[value] = struct{}{}
	}
	return result
}

func daysInMonth(year int, month int) int {
	return time.Date(year, time.Month(month)+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

func formatYearMonth(year int, month int) string {
	return fmt.Sprintf("%04d-%02d", year, month)
}

func formatISODate(year int, month int, day int) string {
	return fmt.Sprintf("%04d-%02d-%02d", year, month, day)
}

func validateInvestPayload(kind TransactionKind, amount float64, details map[string]any, assetType AssetType) (float64, map[string]any, error) {
	assetID, ok := positiveIDFromValue(details["assetId"])
	if !ok {
		return 0, nil, legacy.NewError(legacy.ErrorKindValidation, "detailsJSON.assetId must be a positive number")
	}

	if kind == TransactionKindInvestDividend {
		if amount <= 0 {
			return 0, nil, legacy.NewError(legacy.ErrorKindValidation, "amount must be greater than 0")
		}
		return amount, map[string]any{"assetId": assetID}, nil
	}

	quantity, ok := finiteNumber(details["quantity"])
	if !ok || quantity <= 0 {
		return 0, nil, legacy.NewError(legacy.ErrorKindValidation, "detailsJSON.quantity must be greater than 0")
	}
	price, ok := finiteNumber(details["price"])
	if !ok || price <= 0 {
		return 0, nil, legacy.NewError(legacy.ErrorKindValidation, "detailsJSON.price must be greater than 0")
	}
	commissionAmount, ok := finiteNumber(details["commissionAmount"])
	if !ok && details["commissionAmount"] != nil {
		return 0, nil, legacy.NewError(legacy.ErrorKindValidation, "detailsJSON.commissionAmount must be greater than or equal to 0")
	}
	accruedInterestAmount, ok := finiteNumber(details["accruedInterestAmount"])
	if !ok && details["accruedInterestAmount"] != nil {
		return 0, nil, legacy.NewError(legacy.ErrorKindValidation, "detailsJSON.accruedInterestAmount must be greater than or equal to 0")
	}
	if commissionAmount < 0 {
		return 0, nil, legacy.NewError(legacy.ErrorKindValidation, "detailsJSON.commissionAmount must be greater than or equal to 0")
	}
	if accruedInterestAmount < 0 {
		return 0, nil, legacy.NewError(legacy.ErrorKindValidation, "detailsJSON.accruedInterestAmount must be greater than or equal to 0")
	}
	if assetType != AssetTypeBond && accruedInterestAmount != 0 {
		return 0, nil, legacy.NewError(legacy.ErrorKindValidation, "detailsJSON.accruedInterestAmount is allowed only for bond assets")
	}

	computedAmount := quantity*price - commissionAmount + accruedInterestAmount
	if kind == TransactionKindInvestBuy {
		computedAmount = quantity*price + commissionAmount + accruedInterestAmount
	}
	return computedAmount, map[string]any{
		"assetId":               assetID,
		"quantity":              quantity,
		"price":                 price,
		"commissionAmount":      commissionAmount,
		"accruedInterestAmount": accruedInterestAmount,
	}, nil
}

func marshalNormalizedJSON(value map[string]any) (*string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, legacy.WrapError(legacy.ErrorKindInternal, "Failed to encode detailsJSON", err)
	}
	result := string(encoded)
	return &result, nil
}

func normalizeISODate(value *string, field string) (*string, error) {
	if value == nil {
		return nil, nil
	}
	trimmed := strings.TrimSpace(*value)
	if trimmed == "" {
		return nil, nil
	}
	parsed, err := time.Parse("2006-01-02", trimmed)
	if err != nil || parsed.Format("2006-01-02") != trimmed {
		return nil, legacy.NewError(legacy.ErrorKindValidation, fmt.Sprintf("%s must be a date in YYYY-MM-DD format", field))
	}
	return &trimmed, nil
}

func normalizeAccountIDs(accountIDs []int64) []int64 {
	set := make(map[int64]struct{})
	for _, accountID := range accountIDs {
		if accountID > 0 {
			set[accountID] = struct{}{}
		}
	}
	result := make([]int64, 0, len(set))
	for accountID := range set {
		result = append(result, accountID)
	}
	sort.Slice(result, func(i int, j int) bool { return result[i] < result[j] })
	return result
}

func sameIDList(first []int64, second []int64) bool {
	if len(first) != len(second) {
		return false
	}
	for index := range first {
		if first[index] != second[index] {
			return false
		}
	}
	return true
}

func containsID(values []int64, want int64) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func normalizeLogoBase64(value *string) (*string, error) {
	if value == nil {
		return nil, nil
	}
	trimmed := strings.TrimSpace(*value)
	if trimmed == "" {
		return nil, nil
	}

	decoded, err := base64.StdEncoding.DecodeString(trimmed)
	if err != nil {
		return nil, legacy.NewError(legacy.ErrorKindValidation, "Invalid logoBase64")
	}

	imageData, err := imaging.Decode(bytes.NewReader(decoded), imaging.AutoOrientation(true))
	if err != nil {
		return nil, legacy.NewError(legacy.ErrorKindValidation, "Invalid logoBase64")
	}
	if imageData.Bounds().Dx() <= 32 && imageData.Bounds().Dy() <= 32 {
		return &trimmed, nil
	}

	resized := imaging.Fill(imageData, 32, 32, imaging.Center, imaging.Lanczos)
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, resized); err != nil {
		return nil, legacy.WrapError(legacy.ErrorKindInternal, "Failed to process organization logo", err)
	}
	encoded := base64.StdEncoding.EncodeToString(buffer.Bytes())
	return &encoded, nil
}
