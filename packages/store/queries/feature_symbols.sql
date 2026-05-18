-- name: LinkFeatureSymbol :exec
INSERT OR IGNORE INTO feature_symbols (feature_id, symbol_id, role, source)
VALUES (?, ?, ?, ?);

-- name: ListFeatureSymbolsByFeature :many
SELECT feature_id, symbol_id, role, source
FROM feature_symbols
WHERE feature_id = ?
ORDER BY role, symbol_id;

-- name: ListFeatureSymbolsBySymbol :many
SELECT feature_id, symbol_id, role, source
FROM feature_symbols
WHERE symbol_id = ?
ORDER BY feature_id, role;

-- name: UnlinkFeatureSymbol :execrows
DELETE FROM feature_symbols
WHERE feature_id = ? AND symbol_id = ? AND role = ?;
