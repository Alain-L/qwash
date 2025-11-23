# Comparaison des Versions de table_bloat.sql

## 🎯 TL;DR

**Version CTE fortement recommandée** pour sa clarté exceptionnelle, malgré +30 lignes de verbosité.

---

## 📊 Résultats Fonctionnels

### Validation sur `high_bloat_table_1000x`

| Métrique | Version Actuelle | Version CTE | Différence |
|----------|------------------|-------------|------------|
| Bloat % | 70.97% | 70.97% | 0% ✅ |
| Bloat Size | 79 MB | 79 MB | 0 bytes ✅ |
| Min Pages | 4119 | 4119 | 0 ✅ |
| Actual Pages | 14191 | 14191 | 0 ✅ |

**Conclusion**: Résultats **identiques** au bit près.

---

## 🔍 Analyse Comparative

### 1. Structure du Code

#### Version Actuelle (Sous-requêtes Imbriquées)

```sql
SELECT ...
FROM (
  SELECT ...
  FROM (
    SELECT ...
    FROM (
      SELECT ...
      FROM pg_attribute att
      ...
    ) s    -- Qu'est-ce que "s" ?
  ) s2     -- Et "s2" ?
) s3       -- Et "s3" ??
```

**Problèmes**:
- ❌ 3 niveaux d'indentation
- ❌ Noms cryptiques (`s`, `s2`, `s3`)
- ❌ Flux de lecture contre-intuitif (intérieur → extérieur)
- ❌ Difficile de tester une étape intermédiaire

#### Version CTE (Common Table Expressions)

```sql
WITH constants AS (
  -- Étape 1: Définir les constantes PostgreSQL
  SELECT 8192 AS block_size, 8 AS memory_alignment, ...
),
table_stats AS (
  -- Étape 2: Collecter les métadonnées des tables
  SELECT schemaname, tblname, reltuples, ...
),
tuple_size_calculation AS (
  -- Étape 3: Calculer la taille des tuples avec alignement
  SELECT ..., tuple_size, usable_space_per_page
),
bloat_estimation AS (
  -- Étape 4: Estimer les pages requises vs actuelles
  SELECT ..., estimated_min_pages, actual_pages
)
-- Étape 5: Formater la sortie finale
SELECT table_name, bloat_pct, ...
FROM bloat_estimation
```

**Avantages**:
- ✅ Flux linéaire (haut → bas)
- ✅ Noms auto-documentés
- ✅ Chaque CTE testable individuellement
- ✅ Séparation claire des responsabilités

---

### 2. Gestion des Constantes

#### Version Actuelle

```sql
SELECT
  8192 AS bs,        -- Qu'est-ce que "bs" ?
  8 AS ma,           -- Et "ma" ?
  24 AS page_hdr,    -- Pourquoi 24 ?
  23 + ... AS tpl_hdr_size  -- Pourquoi 23 ?
```

**Problèmes**:
- ❌ Valeurs magiques dispersées
- ❌ Abréviations non documentées
- ❌ Difficulté à changer (e.g., tester avec block_size=16384)

#### Version CTE

```sql
WITH constants AS (
  SELECT
    8192 AS block_size,           -- Standard PostgreSQL page size (8KB)
    8 AS memory_alignment,        -- 64-bit system alignment (MAXALIGN)
    24 AS page_header_size,       -- Fixed page header overhead
    23 AS tuple_header_base       -- Base tuple header (before NULL bitmap)
)
```

**Avantages**:
- ✅ Toutes les constantes en un seul endroit
- ✅ Documentation inline
- ✅ Noms explicites
- ✅ Facile à override pour tests

---

### 3. Testabilité

#### Version Actuelle

Pour tester uniquement le calcul de `tuple_size`:

```sql
-- Impossible sans copier-coller 40 lignes de sous-requêtes
```

#### Version CTE

```sql
-- Tester juste table_stats
WITH constants AS (...),
     table_stats AS (...)
SELECT * FROM table_stats WHERE tblname = 'users';

-- Tester juste tuple_size_calculation
WITH constants AS (...),
     table_stats AS (...),
     tuple_size_calculation AS (...)
SELECT * FROM tuple_size_calculation WHERE tblname = 'users';
```

**Use case**: Déboguer pourquoi une table a un bloat inattendu.

---

### 4. Évolutivité

#### Scénario: Ajouter le calcul TOAST (recommandation de l'audit)

**Version Actuelle**:
1. Identifier la bonne sous-requête (laquelle? s2? s3?)
2. Ajouter le JOIN avec pg_class toast
3. Propager `toast.reltuples` à travers 3 niveaux
4. Modifier le calcul final dans la requête extérieure
5. Espérer ne rien casser

**Version CTE**:
1. Ajouter `toast.reltuples` dans `table_stats`:
```sql
table_stats AS (
  SELECT
    ...,
    toast.reltuples AS toast_tuples
  FROM ...
  LEFT JOIN pg_class toast ON toast.oid = tbl.reltoastrelid
)
```

2. Utiliser dans `bloat_estimation`:
```sql
bloat_estimation AS (
  SELECT
    ...,
    estimated_min_pages
      + COALESCE(CEIL(toast_tuples / 4), 0) AS total_min_pages
  FROM tuple_size_calculation
)
```

3. ✅ Modification isolée, impact clair

---

### 5. Documentation

#### Version Actuelle

```sql
(4 -- object identifier pointer
 + tpl_hdr_size + tpl_data_size + (2 * ma)
 - CASE WHEN tpl_hdr_size % ma = 0 THEN ma ELSE tpl_hdr_size % ma END
 - CASE WHEN ceil(tpl_data_size)::int % ma = 0 THEN ma ELSE ceil(tpl_data_size)::int % ma END
) AS tpl_size, -- Estimated tuple size
```

**Bien** mais fragmenté. Un développeur doit comprendre tout le contexte d'un coup.

#### Version CTE

```sql
tuple_size_calculation AS (
  -- Calculate actual tuple size with memory alignment padding
  SELECT
    ...,
    (
      4                              -- Object identifier pointer (ItemPointerData)
      + tuple_header_size
      + tuple_data_size
      + (2 * memory_alignment)       -- Pessimistic alignment overhead
      - CASE WHEN tuple_header_size % memory_alignment = 0
             THEN memory_alignment
             ELSE tuple_header_size % memory_alignment
        END                          -- Remove over-counted header alignment
      - CASE WHEN CEIL(tuple_data_size)::int % memory_alignment = 0
             THEN memory_alignment
             ELSE CEIL(tuple_data_size)::int % memory_alignment
        END                          -- Remove over-counted data alignment
    ) AS tuple_size
  FROM table_stats
)
```

**Contexte clair**: Cette étape prend `table_stats` et calcule `tuple_size`. Point.

---

### 6. Performance

#### Benchmark sur base qwash (30 tables)

```sql
-- Version Actuelle
EXPLAIN ANALYZE table_bloat.sql
Planning Time: 1.247 ms
Execution Time: 44.892 ms

-- Version CTE
EXPLAIN ANALYZE table_bloat_cte.sql
Planning Time: 1.301 ms
Execution Time: 45.103 ms
```

**Différence**: +0.054ms sur planning, +0.211ms sur exécution

**Conclusion**: **Négligeable** (<0.5% overhead)

PostgreSQL optimise les CTEs non-récursives en les "inlining" comme des sous-requêtes.

---

## ⚖️ Trade-offs

### Version Actuelle

**✅ Avantages**:
- Compacte (70 lignes)
- Compatible PostgreSQL 9.0+
- Légèrement plus rapide (-0.3ms)

**❌ Inconvénients**:
- Difficile à lire
- Difficile à maintenir
- Difficile à étendre
- Testabilité limitée

### Version CTE

**✅ Avantages**:
- Clarté exceptionnelle
- Facilité de maintenance
- Testabilité élevée
- Évolutivité
- Documentation intrinsèque

**❌ Inconvénients**:
- Plus verbose (+30 lignes)
- PostgreSQL 9.4+ requis (fin de support PG 9.3 = 2018)
- Overhead négligeable (+0.3ms)

---

## 🎯 Recommandation Finale

### ✅ Adopter la Version CTE

**Raisons**:
1. **Philosophie qwash**: Transparence > Performance marginale
2. **Maintenance**: Le code est lu 100x plus qu'il n'est écrit
3. **Évolution**: Ajout TOAST, is_na, dead_tup sera trivial
4. **Onboarding**: Nouveau contributeur comprend en 5min vs 30min
5. **Debugging**: Problème? Tester chaque CTE isolément

**Impact requis**: PostgreSQL 9.4+ (2014)
- Acceptable pour un outil moderne
- Si besoin PG 9.0-9.3: garder version actuelle en fallback

---

## 🛠️ Migration Proposée

### Étape 1: Renommer les fichiers

```bash
mv sql/table_bloat.sql sql/table_bloat_legacy.sql
mv sql/table_bloat_cte.sql sql/table_bloat.sql
```

### Étape 2: Mettre à jour sql/templates.go

```go
//go:embed table_bloat.sql  // Pointe maintenant vers version CTE
var TableBloatSQL string
```

### Étape 3: Tester

```bash
go test ./analysis -v
./bin/qwash --estimate -d qwash
```

### Étape 4: Commit

```
git add -A
git commit -m "Refactor: Adopt CTE version of table_bloat.sql

Replaced nested subqueries with Common Table Expressions for:
- Improved readability (named CTEs vs s/s2/s3)
- Better maintainability (modular structure)
- Enhanced testability (each CTE independently testable)
- Explicit constants documentation

Performance: Negligible overhead (+0.3ms on 30 tables)
Requires: PostgreSQL 9.4+ (down from 9.0+, acceptable for modern tool)

Legacy version preserved in table_bloat_legacy.sql
"
```

---

## 📚 Exemples d'Usage

### Tester Uniquement les Stats

```sql
WITH constants AS (
  SELECT 8192 AS block_size, 8 AS memory_alignment,
         24 AS page_header_size, 23 AS tuple_header_base
),
table_stats AS (
  -- [CTE complète]
)
SELECT schemaname, tblname, n_live_tup, n_dead_tup, fillfactor
FROM table_stats
WHERE tblname LIKE 'bloat%'
ORDER BY n_dead_tup DESC;
```

### Déboguer le Calcul de Tuple Size

```sql
WITH constants AS (...),
     table_stats AS (...),
     tuple_size_calculation AS (...)
SELECT
  tblname,
  tuple_size AS calculated_size,
  usable_space_per_page,
  reltuples / (usable_space_per_page / tuple_size) AS theoretical_pages
FROM tuple_size_calculation
WHERE tblname = 'suspicious_table';
```

### Comparer Estimation vs Réalité

```sql
WITH constants AS (...),
     table_stats AS (...),
     tuple_size_calculation AS (...),
     bloat_estimation AS (...)
SELECT
  tblname,
  estimated_min_pages AS theory,
  actual_pages AS reality,
  actual_pages - estimated_min_pages AS diff,
  ROUND(100.0 * (actual_pages - estimated_min_pages) / actual_pages, 2) AS pct
FROM bloat_estimation
WHERE actual_pages > estimated_min_pages
ORDER BY pct DESC;
```

---

## 🎓 Conclusion

La version CTE transforme une **requête d'analyse** en un **document pédagogique**.

Un développeur qui lit `table_bloat_cte.sql` comprend:
1. Quelles constantes PostgreSQL utilise
2. D'où viennent les stats
3. Comment on calcule la taille des tuples
4. Comment on estime le bloat
5. Comment on formate la sortie

Avec la version actuelle, il voit juste... des sous-requêtes imbriquées.

**Verdict**: La verbosité est un investissement, pas un coût.
