package money

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"megaapp-back/internal/platform/sqlite"
)

type Repository struct {
	db    *sql.DB
	write sqlite.WriteDB
}

type txRunner interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func NewRepository(read *sql.DB, write sqlite.WriteDB) *Repository {
	return &Repository{db: read, write: write}
}

func (r *Repository) ListOrganizations(ctx context.Context, userID int64) ([]Organization, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, title, logoBase64
		FROM moneyOrganization
		WHERE userId = ?
		ORDER BY title ASC
	`, userID)
	if err != nil {
		return nil, fmt.Errorf("list organizations: %w", err)
	}
	defer rows.Close()

	var result []Organization
	for rows.Next() {
		var item Organization
		var logo sql.NullString
		if err := rows.Scan(&item.ID, &item.Title, &logo); err != nil {
			return nil, fmt.Errorf("scan organization: %w", err)
		}
		item.LogoBase64 = nullableString(logo)
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate organizations: %w", err)
	}
	return result, nil
}

func (r *Repository) GetOrganizationByID(ctx context.Context, userID int64, organizationID int64) (*Organization, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT id, title, logoBase64
		FROM moneyOrganization
		WHERE id = ? AND userId = ?
	`, organizationID, userID)

	var item Organization
	var logo sql.NullString
	if err := row.Scan(&item.ID, &item.Title, &logo); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("get organization: %w", err)
	}
	item.LogoBase64 = nullableString(logo)
	return &item, nil
}

func (r *Repository) CountAccountsByOrganization(ctx context.Context, userID int64, organizationID int64) (int64, error) {
	var count int64
	if err := r.db.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM moneyAccount
		WHERE organizationId = ? AND userId = ?
	`, organizationID, userID).Scan(&count); err != nil {
		return 0, fmt.Errorf("count accounts by organization: %w", err)
	}
	return count, nil
}

func (r *Repository) CreateOrganization(ctx context.Context, userID int64, input OrganizationInput) (int64, error) {
	result, err := r.write.ExecContext(ctx, `
		INSERT INTO moneyOrganization (title, logoBase64, userId)
		VALUES (?, ?, ?)
	`, input.Title, valueOrNil(input.LogoBase64), userID)
	if err != nil {
		return 0, fmt.Errorf("create organization: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("organization last insert id: %w", err)
	}
	return id, nil
}

func (r *Repository) UpdateOrganization(ctx context.Context, userID int64, organizationID int64, input OrganizationInput) error {
	_, err := r.write.ExecContext(ctx, `
		UPDATE moneyOrganization
		SET title = ?, logoBase64 = ?
		WHERE id = ? AND userId = ?
	`, input.Title, valueOrNil(input.LogoBase64), organizationID, userID)
	if err != nil {
		return fmt.Errorf("update organization: %w", err)
	}
	return nil
}

func (r *Repository) DeleteOrganization(ctx context.Context, userID int64, organizationID int64) error {
	_, err := r.write.ExecContext(ctx, `
		DELETE FROM moneyOrganization
		WHERE id = ? AND userId = ?
	`, organizationID, userID)
	if err != nil {
		return fmt.Errorf("delete organization: %w", err)
	}
	return nil
}

func (r *Repository) ListCurrencies(ctx context.Context, userID int64) ([]Currency, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, title, ticker, symbol, symbolPosEnum, whitespace
		FROM moneyCurrency
		WHERE userId = ?
		ORDER BY title ASC
	`, userID)
	if err != nil {
		return nil, fmt.Errorf("list currencies: %w", err)
	}
	defer rows.Close()

	var result []Currency
	for rows.Next() {
		var item Currency
		var whitespace int64
		if err := rows.Scan(&item.ID, &item.Title, &item.Ticker, &item.Symbol, &item.SymbolPosEnum, &whitespace); err != nil {
			return nil, fmt.Errorf("scan currency: %w", err)
		}
		item.Whitespace = whitespace != 0
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate currencies: %w", err)
	}
	return result, nil
}

func (r *Repository) GetCurrencyByID(ctx context.Context, userID int64, currencyID int64) (*Currency, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT id, title, ticker, symbol, symbolPosEnum, whitespace
		FROM moneyCurrency
		WHERE id = ? AND userId = ?
	`, currencyID, userID)

	var item Currency
	var whitespace int64
	if err := row.Scan(&item.ID, &item.Title, &item.Ticker, &item.Symbol, &item.SymbolPosEnum, &whitespace); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("get currency: %w", err)
	}
	item.Whitespace = whitespace != 0
	return &item, nil
}

func (r *Repository) CountAccountsByCurrency(ctx context.Context, userID int64, currencyID int64) (int64, error) {
	var count int64
	if err := r.db.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM moneyAccount
		WHERE currencyId = ? AND userId = ?
	`, currencyID, userID).Scan(&count); err != nil {
		return 0, fmt.Errorf("count accounts by currency: %w", err)
	}
	return count, nil
}

func (r *Repository) CreateCurrency(ctx context.Context, userID int64, input CurrencyInput) (int64, error) {
	result, err := r.write.ExecContext(ctx, `
		INSERT INTO moneyCurrency (title, ticker, symbol, symbolPosEnum, whitespace, userId)
		VALUES (?, ?, ?, ?, ?, ?)
	`, input.Title, input.Ticker, input.Symbol, input.SymbolPosEnum, boolToInt64(input.Whitespace), userID)
	if err != nil {
		return 0, fmt.Errorf("create currency: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("currency last insert id: %w", err)
	}
	return id, nil
}

func (r *Repository) UpdateCurrency(ctx context.Context, userID int64, currencyID int64, input CurrencyInput) error {
	_, err := r.write.ExecContext(ctx, `
		UPDATE moneyCurrency
		SET title = ?, ticker = ?, symbol = ?, symbolPosEnum = ?, whitespace = ?
		WHERE id = ? AND userId = ?
	`, input.Title, input.Ticker, input.Symbol, input.SymbolPosEnum, boolToInt64(input.Whitespace), currencyID, userID)
	if err != nil {
		return fmt.Errorf("update currency: %w", err)
	}
	return nil
}

func (r *Repository) DeleteCurrency(ctx context.Context, userID int64, currencyID int64) error {
	_, err := r.write.ExecContext(ctx, `
		DELETE FROM moneyCurrency
		WHERE id = ? AND userId = ?
	`, currencyID, userID)
	if err != nil {
		return fmt.Errorf("delete currency: %w", err)
	}
	return nil
}

func (r *Repository) ListCategories(ctx context.Context, userID int64) ([]Category, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, name, parentId, categoryType, hiddenOnCharts, hue, lightness, saturation
		FROM moneyCategories
		WHERE userId = ?
		ORDER BY categoryType ASC, parentId ASC, name ASC
	`, userID)
	if err != nil {
		return nil, fmt.Errorf("list categories: %w", err)
	}
	defer rows.Close()

	var result []Category
	for rows.Next() {
		item, err := scanCategory(rows)
		if err != nil {
			return nil, fmt.Errorf("scan category: %w", err)
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate categories: %w", err)
	}
	return result, nil
}

func (r *Repository) GetCategoryByID(ctx context.Context, userID int64, categoryID int64) (*Category, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT id, name, parentId, categoryType, hiddenOnCharts, hue, lightness, saturation
		FROM moneyCategories
		WHERE id = ? AND userId = ?
	`, categoryID, userID)

	item, err := scanCategory(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("get category: %w", err)
	}
	return &item, nil
}

func (r *Repository) CountChildCategories(ctx context.Context, userID int64, categoryID int64) (int64, error) {
	var count int64
	if err := r.db.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM moneyCategories
		WHERE parentId = ? AND userId = ?
	`, categoryID, userID).Scan(&count); err != nil {
		return 0, fmt.Errorf("count child categories: %w", err)
	}
	return count, nil
}

func (r *Repository) CountTransactionsByCategory(ctx context.Context, userID int64, categoryID int64) (int64, error) {
	var count int64
	if err := r.db.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM moneyTransaction
		WHERE categoryId = ? AND userId = ?
	`, categoryID, userID).Scan(&count); err != nil {
		return 0, fmt.Errorf("count transactions by category: %w", err)
	}
	return count, nil
}

func (r *Repository) CreateCategory(ctx context.Context, userID int64, input CategoryInput) (int64, error) {
	result, err := r.write.ExecContext(ctx, `
		INSERT INTO moneyCategories (name, categoryType, userId, parentId, hiddenOnCharts, hue, lightness, saturation)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, input.Name, input.CategoryType, userID, nullableValue(input.ParentID), boolToInt64(input.HiddenOnCharts), nullableValue(input.Hue), nullableValue(input.Lightness), nullableValue(input.Saturation))
	if err != nil {
		return 0, fmt.Errorf("create category: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("category last insert id: %w", err)
	}
	return id, nil
}

func (r *Repository) UpdateCategory(ctx context.Context, userID int64, categoryID int64, input CategoryInput) error {
	_, err := r.write.ExecContext(ctx, `
		UPDATE moneyCategories
		SET name = ?, categoryType = ?, parentId = ?, hiddenOnCharts = ?, hue = ?, lightness = ?, saturation = ?
		WHERE id = ? AND userId = ?
	`, input.Name, input.CategoryType, nullableValue(input.ParentID), boolToInt64(input.HiddenOnCharts), nullableValue(input.Hue), nullableValue(input.Lightness), nullableValue(input.Saturation), categoryID, userID)
	if err != nil {
		return fmt.Errorf("update category: %w", err)
	}
	return nil
}

func (r *Repository) DeleteCategory(ctx context.Context, userID int64, categoryID int64) error {
	_, err := r.write.ExecContext(ctx, `
		DELETE FROM moneyCategories
		WHERE id = ? AND userId = ?
	`, categoryID, userID)
	if err != nil {
		return fmt.Errorf("delete category: %w", err)
	}
	return nil
}

func (r *Repository) ListAccounts(ctx context.Context, userID int64) ([]Account, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, title, currencyId, isInvest, isArchived, kind, organizationId
		FROM moneyAccount
		WHERE userId = ?
		ORDER BY title ASC
	`, userID)
	if err != nil {
		return nil, fmt.Errorf("list accounts: %w", err)
	}
	defer rows.Close()

	var result []Account
	for rows.Next() {
		var item Account
		var isInvest int64
		var isArchived int64
		var organizationID sql.NullInt64
		if err := rows.Scan(&item.ID, &item.Title, &item.CurrencyID, &isInvest, &isArchived, &item.Kind, &organizationID); err != nil {
			return nil, fmt.Errorf("scan account: %w", err)
		}
		item.IsInvest = isInvest != 0
		item.IsArchived = isArchived != 0
		item.OrganizationID = nullableInt64(organizationID)
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate accounts: %w", err)
	}
	return result, nil
}

func (r *Repository) GetAccountByID(ctx context.Context, userID int64, accountID int64) (*Account, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT id, title, currencyId, isInvest, isArchived, kind, organizationId
		FROM moneyAccount
		WHERE id = ? AND userId = ?
	`, accountID, userID)

	var item Account
	var isInvest int64
	var isArchived int64
	var organizationID sql.NullInt64
	if err := row.Scan(&item.ID, &item.Title, &item.CurrencyID, &isInvest, &isArchived, &item.Kind, &organizationID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("get account: %w", err)
	}
	item.IsInvest = isInvest != 0
	item.IsArchived = isArchived != 0
	item.OrganizationID = nullableInt64(organizationID)
	return &item, nil
}

func (r *Repository) CountTransactionsByAccount(ctx context.Context, userID int64, accountID int64) (int64, error) {
	var count int64
	if err := r.db.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM moneyTransaction
		WHERE accountId = ? AND userId = ?
	`, accountID, userID).Scan(&count); err != nil {
		return 0, fmt.Errorf("count transactions by account: %w", err)
	}
	return count, nil
}

func (r *Repository) CountAssetsByAccount(ctx context.Context, userID int64, accountID int64) (int64, error) {
	var count int64
	if err := r.db.QueryRowContext(ctx, `
		SELECT COUNT(DISTINCT a.id)
		FROM moneyAsset a, json_each(a.accountIdsJSON) j
		WHERE a.userId = ? AND CAST(j.value AS INTEGER) = ?
	`, userID, accountID).Scan(&count); err != nil {
		return 0, fmt.Errorf("count assets by account: %w", err)
	}
	return count, nil
}

func (r *Repository) CreateAccount(ctx context.Context, userID int64, input AccountInput) (int64, error) {
	result, err := r.write.ExecContext(ctx, `
		INSERT INTO moneyAccount (title, currencyId, isInvest, isArchived, kind, organizationId, userId)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, input.Title, input.CurrencyID, boolToInt64(input.IsInvest), boolToInt64(input.IsArchived), input.Kind, nullableValue(input.OrganizationID), userID)
	if err != nil {
		return 0, fmt.Errorf("create account: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("account last insert id: %w", err)
	}
	return id, nil
}

func (r *Repository) UpdateAccount(ctx context.Context, userID int64, accountID int64, input AccountInput) error {
	_, err := r.write.ExecContext(ctx, `
		UPDATE moneyAccount
		SET title = ?, currencyId = ?, isInvest = ?, isArchived = ?, kind = ?, organizationId = ?
		WHERE id = ? AND userId = ?
	`, input.Title, input.CurrencyID, boolToInt64(input.IsInvest), boolToInt64(input.IsArchived), input.Kind, nullableValue(input.OrganizationID), accountID, userID)
	if err != nil {
		return fmt.Errorf("update account: %w", err)
	}
	return nil
}

func (r *Repository) DeleteAccount(ctx context.Context, userID int64, accountID int64) error {
	_, err := r.write.ExecContext(ctx, `
		DELETE FROM moneyAccount
		WHERE id = ? AND userId = ?
	`, accountID, userID)
	if err != nil {
		return fmt.Errorf("delete account: %w", err)
	}
	return nil
}

func (r *Repository) ListAssets(ctx context.Context, userID int64) ([]Asset, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, title, ticker, type, accountIdsJSON, suspendedSince, suspendedUntil
		FROM moneyAsset
		WHERE userId = ?
		ORDER BY title ASC
	`, userID)
	if err != nil {
		return nil, fmt.Errorf("list assets: %w", err)
	}
	defer rows.Close()

	var result []Asset
	for rows.Next() {
		item, err := scanAsset(rows)
		if err != nil {
			return nil, fmt.Errorf("scan asset: %w", err)
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate assets: %w", err)
	}
	return result, nil
}

func (r *Repository) GetAssetByID(ctx context.Context, userID int64, assetID int64) (*Asset, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT id, title, ticker, type, accountIdsJSON, suspendedSince, suspendedUntil
		FROM moneyAsset
		WHERE id = ? AND userId = ?
	`, assetID, userID)

	item, err := scanAsset(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("get asset: %w", err)
	}
	return &item, nil
}

func (r *Repository) CountTransactionsByAsset(ctx context.Context, userID int64, assetID int64) (int64, error) {
	var count int64
	if err := r.db.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM moneyTransaction
		WHERE userId = ?
		  AND detailsJSON IS NOT NULL
		  AND json_valid(detailsJSON) = 1
		  AND CAST(json_extract(detailsJSON, '$.assetId') AS INTEGER) = ?
	`, userID, assetID).Scan(&count); err != nil {
		return 0, fmt.Errorf("count transactions by asset: %w", err)
	}
	return count, nil
}

func (r *Repository) ListLinkedTransactionAccountIDsByAsset(ctx context.Context, userID int64, assetID int64) ([]int64, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT DISTINCT accountId
		FROM moneyTransaction
		WHERE userId = ?
		  AND detailsJSON IS NOT NULL
		  AND json_valid(detailsJSON) = 1
		  AND CAST(json_extract(detailsJSON, '$.assetId') AS INTEGER) = ?
		ORDER BY accountId ASC
	`, userID, assetID)
	if err != nil {
		return nil, fmt.Errorf("list linked transaction account ids by asset: %w", err)
	}
	defer rows.Close()

	var result []int64
	for rows.Next() {
		var accountID int64
		if err := rows.Scan(&accountID); err != nil {
			return nil, fmt.Errorf("scan linked transaction account id: %w", err)
		}
		result = append(result, accountID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate linked transaction account ids by asset: %w", err)
	}
	return result, nil
}

func (r *Repository) CreateAsset(ctx context.Context, userID int64, input AssetInput) (int64, error) {
	result, err := r.write.ExecContext(ctx, `
		INSERT INTO moneyAsset (title, ticker, type, accountIdsJSON, suspendedSince, suspendedUntil, userId)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, input.Title, input.Ticker, input.Type, marshalAccountIDs(input.AccountIDs), valueOrNil(input.SuspendedSince), valueOrNil(input.SuspendedUntil), userID)
	if err != nil {
		return 0, fmt.Errorf("create asset: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("asset last insert id: %w", err)
	}
	return id, nil
}

func (r *Repository) UpdateAsset(ctx context.Context, userID int64, assetID int64, input AssetInput) error {
	_, err := r.write.ExecContext(ctx, `
		UPDATE moneyAsset
		SET title = ?, ticker = ?, type = ?, accountIdsJSON = ?, suspendedSince = ?, suspendedUntil = ?
		WHERE id = ? AND userId = ?
	`, input.Title, input.Ticker, input.Type, marshalAccountIDs(input.AccountIDs), valueOrNil(input.SuspendedSince), valueOrNil(input.SuspendedUntil), assetID, userID)
	if err != nil {
		return fmt.Errorf("update asset: %w", err)
	}
	return nil
}

func (r *Repository) DeleteAsset(ctx context.Context, userID int64, assetID int64) error {
	_, err := r.write.ExecContext(ctx, `
		DELETE FROM moneyAsset
		WHERE id = ? AND userId = ?
	`, assetID, userID)
	if err != nil {
		return fmt.Errorf("delete asset: %w", err)
	}
	return nil
}

func (r *Repository) ListTransactions(ctx context.Context, userID int64) ([]Transaction, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, dateISO, accountId, amount, categoryId, kind, isGift, notes, detailsJSON, twinId, ver
		FROM moneyTransaction
		WHERE userId = ?
		ORDER BY dateISO DESC, id DESC
	`, userID)
	if err != nil {
		return nil, fmt.Errorf("list transactions: %w", err)
	}
	defer rows.Close()

	var result []Transaction
	for rows.Next() {
		item, err := scanTransaction(rows)
		if err != nil {
			return nil, fmt.Errorf("scan transaction: %w", err)
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate transactions: %w", err)
	}
	return result, nil
}

func (r *Repository) GetTransactionByID(ctx context.Context, runner txRunner, userID int64, transactionID int64) (*Transaction, error) {
	row := runner.QueryRowContext(ctx, `
		SELECT id, dateISO, accountId, amount, categoryId, kind, isGift, notes, detailsJSON, twinId, ver
		FROM moneyTransaction
		WHERE id = ? AND userId = ?
	`, transactionID, userID)

	item, err := scanTransaction(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("get transaction: %w", err)
	}
	return &item, nil
}

func (r *Repository) CreateTransaction(ctx context.Context, tx txRunner, userID int64, input TransactionInput) (int64, error) {
	return createTransactionWithRunner(ctx, tx, userID, input, input.StoredDetailsJSON)
}

func (r *Repository) CreateTransferPair(ctx context.Context, tx txRunner, userID int64, input TransactionInput) (CreateTransactionResult, error) {
	fromDetails := `{"direction":"out"}`
	fromID, err := createTransactionWithRunner(ctx, tx, userID, input, &fromDetails)
	if err != nil {
		return CreateTransactionResult{}, err
	}

	toDetails := `{"direction":"in"}`
	toInput := TransactionInput{
		DateISO:    input.DateISO,
		AccountID:  *input.TwinAccountID,
		Amount:     *input.TwinAmount,
		CategoryID: nil,
		Kind:       TransactionKindTransfer,
		IsGift:     false,
		Notes:      input.Notes,
	}
	toID, err := createTransactionWithRunner(ctx, tx, userID, toInput, &toDetails)
	if err != nil {
		return CreateTransactionResult{}, err
	}

	if err := updateTwinIDWithRunner(ctx, tx, userID, fromID, &toID); err != nil {
		return CreateTransactionResult{}, err
	}
	if err := updateTwinIDWithRunner(ctx, tx, userID, toID, &fromID); err != nil {
		return CreateTransactionResult{}, err
	}

	return CreateTransactionResult{ID: fromID, TwinID: &toID, Version: 1}, nil
}

func (r *Repository) UpdateTransaction(ctx context.Context, tx txRunner, userID int64, transactionID int64, input TransactionInput, newVersion int64) error {
	result, err := tx.ExecContext(ctx, `
		UPDATE moneyTransaction
		SET dateISO = ?, accountId = ?, amount = ?, categoryId = ?, kind = ?, isGift = ?, notes = ?, detailsJSON = ?, ver = ?
		WHERE id = ? AND userId = ?
	`, input.DateISO, input.AccountID, input.Amount, nullableValue(input.CategoryID), input.Kind, boolToInt64(input.IsGift), valueOrNil(input.Notes), createTransactionDetailsValue(input.StoredDetailsJSON), newVersion, transactionID, userID)
	if err != nil {
		return fmt.Errorf("update transaction: %w", err)
	}
	changedRows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("transaction rows affected: %w", err)
	}
	if changedRows == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (r *Repository) UpdateTransferPair(ctx context.Context, tx txRunner, userID int64, fromID int64, toID int64, input TransactionInput, fromNewVersion int64, toNewVersion int64) error {
	if err := updateTransferRowWithRunner(ctx, tx, userID, fromID, input.DateISO, input.Amount, input.Notes, fromNewVersion); err != nil {
		return err
	}
	if err := updateTransferRowWithRunner(ctx, tx, userID, toID, input.DateISO, *input.TwinAmount, input.Notes, toNewVersion); err != nil {
		return err
	}
	return nil
}

func (r *Repository) DeleteTransaction(ctx context.Context, tx txRunner, userID int64, transactionID int64) error {
	result, err := tx.ExecContext(ctx, `
		DELETE FROM moneyTransaction
		WHERE id = ? AND userId = ?
	`, transactionID, userID)
	if err != nil {
		return fmt.Errorf("delete transaction: %w", err)
	}
	changedRows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete transaction rows affected: %w", err)
	}
	if changedRows == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (r *Repository) ListInvestAssetTrades(ctx context.Context, userID int64) ([]InvestAssetTrade, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT t.id, t.dateISO, t.accountId, t.amount, t.kind, t.notes, t.detailsJSON,
		       a.id as assetId, a.title as assetTitle, a.ticker as assetTicker, a.type as assetType
		FROM moneyTransaction t
		LEFT JOIN moneyAsset a
		  ON a.id = CAST(json_extract(t.detailsJSON, '$.assetId') AS INTEGER)
		 AND a.userId = t.userId
		WHERE t.userId = ?
		  AND t.kind IN ('invest_buy', 'invest_sell')
		ORDER BY t.dateISO DESC, t.id DESC
	`, userID)
	if err != nil {
		return nil, fmt.Errorf("list invest asset trades: %w", err)
	}
	defer rows.Close()

	var result []InvestAssetTrade
	for rows.Next() {
		var item InvestAssetTrade
		var notes sql.NullString
		var detailsJSON sql.NullString
		var assetID sql.NullInt64
		var assetTitle sql.NullString
		var assetTicker sql.NullString
		var assetType sql.NullString
		if err := rows.Scan(&item.ID, &item.DateISO, &item.AccountID, &item.Amount, &item.Kind, &notes, &detailsJSON, &assetID, &assetTitle, &assetTicker, &assetType); err != nil {
			return nil, fmt.Errorf("scan invest asset trade: %w", err)
		}
		item.Notes = nullableString(notes)
		item.DetailsJSON = nullableString(detailsJSON)
		item.AssetID = nullableInt64(assetID)
		item.AssetTitle = nullableString(assetTitle)
		item.AssetTicker = nullableString(assetTicker)
		item.AssetType = nullableString(assetType)
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate invest asset trades: %w", err)
	}
	return result, nil
}

func (r *Repository) ListRateHistory(ctx context.Context) ([]RateHistory, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, dateISO, ratesJson
		FROM moneyRateHistory
		ORDER BY dateISO ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("list rate history: %w", err)
	}
	defer rows.Close()

	var result []RateHistory
	for rows.Next() {
		var item RateHistory
		if err := rows.Scan(&item.ID, &item.DateISO, &item.RatesJSON); err != nil {
			return nil, fmt.Errorf("scan rate history: %w", err)
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate rate history: %w", err)
	}
	return result, nil
}

func createTransactionDetailsValue(detailsJSON *string) any {
	if detailsJSON == nil {
		return nil
	}
	return *detailsJSON
}

func scanAsset(scanner interface{ Scan(dest ...any) error }) (Asset, error) {
	var item Asset
	var accountIDsJSON string
	var suspendedSince sql.NullString
	var suspendedUntil sql.NullString
	if err := scanner.Scan(&item.ID, &item.Title, &item.Ticker, &item.Type, &accountIDsJSON, &suspendedSince, &suspendedUntil); err != nil {
		return Asset{}, err
	}
	accountIDs, err := parseAccountIDs(accountIDsJSON)
	if err != nil {
		return Asset{}, fmt.Errorf("parse asset account ids: %w", err)
	}
	item.AccountIDs = accountIDs
	item.SuspendedSince = nullableString(suspendedSince)
	item.SuspendedUntil = nullableString(suspendedUntil)
	return item, nil
}

func scanCategory(scanner interface{ Scan(dest ...any) error }) (Category, error) {
	var item Category
	var parentID sql.NullInt64
	var hue, lightness, saturation sql.NullInt64
	var hiddenOnCharts int64
	if err := scanner.Scan(&item.ID, &item.Name, &parentID, &item.CategoryType, &hiddenOnCharts, &hue, &lightness, &saturation); err != nil {
		return Category{}, err
	}
	item.ParentID = nullableInt64(parentID)
	item.HiddenOnCharts = hiddenOnCharts != 0
	item.Hue = nullableInt64(hue)
	item.Lightness = nullableInt64(lightness)
	item.Saturation = nullableInt64(saturation)
	return item, nil
}

func scanTransaction(scanner interface{ Scan(dest ...any) error }) (Transaction, error) {
	var item Transaction
	var categoryID sql.NullInt64
	var isGift int64
	var notes sql.NullString
	var detailsJSON sql.NullString
	var twinID sql.NullInt64
	if err := scanner.Scan(&item.ID, &item.DateISO, &item.AccountID, &item.Amount, &categoryID, &item.Kind, &isGift, &notes, &detailsJSON, &twinID, &item.Version); err != nil {
		return Transaction{}, err
	}
	item.CategoryID = nullableInt64(categoryID)
	item.IsGift = isGift != 0
	item.Notes = nullableString(notes)
	item.DetailsJSON = nullableString(detailsJSON)
	item.TwinID = nullableInt64(twinID)
	return item, nil
}

func parseAccountIDs(value string) ([]int64, error) {
	var raw []any
	if err := json.Unmarshal([]byte(value), &raw); err != nil {
		return nil, err
	}

	set := make(map[int64]struct{})
	for _, item := range raw {
		switch value := item.(type) {
		case float64:
			id := int64(value)
			if value == float64(id) && id > 0 {
				set[id] = struct{}{}
			}
		}
	}

	result := make([]int64, 0, len(set))
	for id := range set {
		result = append(result, id)
	}
	sort.Slice(result, func(i int, j int) bool { return result[i] < result[j] })
	return result, nil
}

func createTransactionWithRunner(ctx context.Context, runner txRunner, userID int64, input TransactionInput, detailsJSON *string) (int64, error) {
	result, err := runner.ExecContext(ctx, `
		INSERT INTO moneyTransaction (dateISO, accountId, amount, categoryId, kind, isGift, notes, detailsJSON, userId, twinId)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, NULL)
	`, input.DateISO, input.AccountID, input.Amount, nullableValue(input.CategoryID), input.Kind, boolToInt64(input.IsGift), valueOrNil(input.Notes), createTransactionDetailsValue(detailsJSON), userID)
	if err != nil {
		return 0, fmt.Errorf("create transaction: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("transaction last insert id: %w", err)
	}
	return id, nil
}

func updateTwinIDWithRunner(ctx context.Context, runner txRunner, userID int64, transactionID int64, twinID *int64) error {
	result, err := runner.ExecContext(ctx, `
		UPDATE moneyTransaction
		SET twinId = ?
		WHERE id = ? AND userId = ?
	`, nullableValue(twinID), transactionID, userID)
	if err != nil {
		return fmt.Errorf("update transaction twin id: %w", err)
	}
	changedRows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("transaction twin rows affected: %w", err)
	}
	if changedRows == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func updateTransferRowWithRunner(ctx context.Context, runner txRunner, userID int64, transactionID int64, dateISO string, amount float64, notes *string, newVersion int64) error {
	result, err := runner.ExecContext(ctx, `
		UPDATE moneyTransaction
		SET dateISO = ?, amount = ?, notes = ?, ver = ?
		WHERE id = ? AND userId = ?
	`, dateISO, amount, valueOrNil(notes), newVersion, transactionID, userID)
	if err != nil {
		return fmt.Errorf("update transfer row: %w", err)
	}
	changedRows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("transfer row rows affected: %w", err)
	}
	if changedRows == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func marshalAccountIDs(accountIDs []int64) string {
	encoded, err := json.Marshal(accountIDs)
	if err != nil {
		return "[]"
	}
	return string(encoded)
}

func nullableString(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	result := value.String
	return &result
}

func nullableInt64(value sql.NullInt64) *int64 {
	if !value.Valid {
		return nil
	}
	result := value.Int64
	return &result
}

func nullableValue(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}

func valueOrNil(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}

func boolToInt64(value bool) int64 {
	if value {
		return 1
	}
	return 0
}
