ALTER TABLE moneyCategories ADD COLUMN hiddenOnCharts INTEGER NOT NULL DEFAULT 0;
ALTER TABLE moneyCategories ADD COLUMN hue INTEGER CHECK(hue IS NULL OR hue BETWEEN 0 AND 360);

-- Data fix: an expense/income transaction must sit in a category of its own type. Old rows broke
-- that (the check exists only on writes), and the charts, now driven by category properties instead
-- of category names, show such a row as a stray series. Re-point it to the same-named category of the
-- right type (an expense "Технический гэп" filed under the income "Технический гэп"); bump ver so
-- clients refetch it.
UPDATE moneyTransaction
SET categoryId = (
        SELECT dst.id
        FROM moneyCategories src
        JOIN moneyCategories dst ON dst.userId = src.userId AND dst.name = src.name AND dst.categoryType = moneyTransaction.kind
        WHERE src.id = moneyTransaction.categoryId
    ),
    ver = ver + 1
WHERE kind IN ('expense', 'income')
  AND EXISTS (
        SELECT 1
        FROM moneyCategories src
        JOIN moneyCategories dst ON dst.userId = src.userId AND dst.name = src.name AND dst.categoryType = moneyTransaction.kind
        WHERE src.id = moneyTransaction.categoryId AND src.categoryType <> moneyTransaction.kind
    );

-- The remaining mismatch has no same-named category to move to: the 10000 income of 2024-07-13 filed
-- under the expense "Подарок" is a received gift, i.e. the income category "Дар".
UPDATE moneyTransaction
SET categoryId = (
        SELECT gift.id FROM moneyCategories gift
        WHERE gift.userId = moneyTransaction.userId AND gift.name = 'Дар' AND gift.categoryType = 'income'
    ),
    ver = ver + 1
WHERE id = 2613
  AND kind = 'income'
  AND EXISTS (SELECT 1 FROM moneyCategories src WHERE src.id = moneyTransaction.categoryId AND src.categoryType <> 'income')
  AND EXISTS (SELECT 1 FROM moneyCategories gift WHERE gift.userId = moneyTransaction.userId AND gift.name = 'Дар' AND gift.categoryType = 'income');
