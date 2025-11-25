# Audit des Requêtes ioguix vs qwash

## 📊 Résumé Exécutif

### Comparaison Table Bloat
- **ioguix**: Bloat de 72.98% (84.8 MB)
- **qwash**: Bloat de 70.97% (82.5 MB)
- **Différence**: 2% (2.3 MB) - Négligeable

Les deux approches donnent des résultats **très similaires** avec une différence <3%, ce qui est excellent pour la confiance.

---

## 🔍 1. Analyse de `table_bloat.sql` (ioguix)

### ✅ Qualités

1. **Algorithme robuste et éprouvé**
   - Utilisé en production depuis des années
   - Calcule le bloat sans extension (pas besoin de pgstattuple)
   - Compatible PostgreSQL 9.0+

2. **Précision mathématique**
   - Calcul exact de la taille des tuples avec alignement mémoire (MAXALIGN)
   - Prise en compte du fillfactor
   - Calcul séparé pour TOAST

3. **Gestion TOAST**
   ```sql
   ceil( reltuples / ( (bs-page_hdr)/tpl_size ) ) + ceil( toasttuples / 4 )
   ```
   - Ajoute les pages TOAST estimées (4 tuples/page)
   - Important pour les tables avec colonnes larges

4. **Détection architecture**
   ```sql
   CASE WHEN version()~'mingw32' OR version()~'64-bit|x86_64|ppc64|ia64|amd64'
        THEN 8 ELSE 4 END AS ma
   ```
   - Adapte l'alignement mémoire selon l'architecture (32-bit vs 64-bit)

### ❌ Limites

1. **Lisibilité difficile**
   - 3 niveaux de sous-requêtes imbriquées
   - Noms de colonnes peu explicites (`tpl_hdr_size`, `est_tblpages_ff`)
   - Difficile à maintenir/modifier

2. **Flag `is_na` opaque**
   ```sql
   bool_or(att.atttypid = 'pg_catalog.name'::regtype)
     OR sum(CASE WHEN att.attnum > 0 THEN 1 ELSE 0 END) <> count(s.attname) AS is_na
   ```
   - Marque les tables avec type `name` ou stats manquantes
   - Pas d'explication dans les commentaires
   - L'utilisateur ne sait pas pourquoi certaines tables sont marquées

3. **Pas de calcul dead_tup**
   - Manque le nombre de tuples morts
   - Utile pour diagnostiquer les problèmes de VACUUM

4. **Sortie brute**
   - Inclut toutes les tables système (pg_catalog, information_schema)
   - Pas de filtrage par défaut
   - Pas de formatage human-readable des tailles

---

## 🔍 2. Analyse de `table_bloat.sql` (qwash)

### ✅ Qualités

1. **Lisibilité exceptionnelle**
   - Commentaires détaillés sur chaque calcul
   - Noms de colonnes explicites (`min_pages_required`, `actual_pages`)
   - Structure en 3 CTEs claires

2. **Informations enrichies**
   ```sql
   n_live_tup AS live_tup,
   (GREATEST(0, tblpages - est_tblpages) * ...) AS dead_tup,
   ```
   - Calcule `dead_tup` (tuples morts estimés)
   - Utile pour détecter les problèmes de VACUUM

3. **Sortie optimisée**
   - Format `schema.table` directement
   - Tailles formatées avec `pg_size_pretty()`
   - Taille TOAST séparée et formatée
   - Tri par bloat_pct DESC
   - Filtre les schémas système par défaut

4. **Simplifications judicieuses**
   - `bs = 8192` hardcodé (standard PostgreSQL)
   - `ma = 8` hardcodé (64-bit assumé)
   - Réduit la complexité sans perte de précision significative

5. **Join avec pg_stat_user_tables**
   ```sql
   JOIN pg_stat_user_tables psut ON psut.relid = tbl.oid
   ```
   - Accès direct à `n_live_tup` et `n_dead_tup`
   - Plus fiable que les estimations

### ⚠️ Limites Potentielles

1. **Pas de gestion TOAST dans le calcul**
   - La version ioguix ajoute `+ ceil( toasttuples / 4 )`
   - qwash ne l'inclut pas dans `est_tblpages`
   - **Impact**: Sous-estime le bloat pour tables avec TOAST volumineuse
   - **Mitigation**: Affiche la taille TOAST séparément

2. **Hardcodé pour 64-bit**
   - `ma = 8` suppose architecture 64-bit
   - Rare problème (presque tous les serveurs modernes sont 64-bit)

3. **Pas de flag is_na**
   - Ne détecte pas les cas où les stats sont invalides
   - Pourrait donner des résultats faux si `ANALYZE` jamais exécuté

4. **Dépendance pg_stat_user_tables**
   - Requiert que les stats soient à jour
   - Si autovacuum désactivé, peut être inexact

---

## 📈 3. Tests de Validation

### Table: `high_bloat_table_1000x`

| Métrique   | ioguix      | qwash       | Différence       |
| ---------- | ----------- | ----------- | ---------------- |
| Real size  | 116,252,672 | 116,252,672 | 0 bytes          |
| Bloat size | 84,836,352  | 82,509,824  | 2,326,528 (2.7%) |
| Bloat %    | 72.98%      | 70.97%      | -2.01%           |
| is_na      | false       | N/A         | -                |

**Analyse**:
- Différence de 2.3 MB sur 84 MB = **2.7% d'écart**
- Probablement dû à l'absence de calcul TOAST dans qwash
- Reste **acceptable** pour un outil d'estimation

### Validation Fillfactor

Table `high_bloat_table_10x_fill80` (fillfactor=80):

| Métrique           | ioguix | qwash  |
| ------------------ | ------ | ------ |
| Bloat %            | 72.29% | 69.88% |
| Fillfactor détecté | 80     | 80     |

✅ Les deux détectent correctement le fillfactor et l'utilisent dans le calcul.

---

## 🔧 4. Analyse de `btree_bloat.sql` (ioguix)

### ✅ Qualités

1. **Algorithme B-tree spécifique**
   - Calcule l'overhead des pages internes B-tree
   - Prend en compte `pageopqdata` (16 bytes d'opaque data)
   - Fillfactor par défaut = 90 (vs 100 pour tables)

2. **Support multi-colonnes**
   ```sql
   pg_catalog.generate_series(1,indnatts) AS attpos
   ```
   - Itère sur chaque colonne de l'index
   - Calcule la taille totale avec padding

3. **Gestion cas spéciaux**
   ```sql
   CASE WHEN a1.attnum IS NULL THEN ic.idxname ELSE ct.relname END AS attrelname
   ```
   - Différencie colonnes table vs colonnes expression

### ❌ Limites

1. **Complexité extrême**
   - 5 niveaux de sous-requêtes imbriquées
   - Presque impossible à déboguer
   - Très difficile à adapter

2. **Btree only**
   ```sql
   WHERE ci.relam=(SELECT oid FROM pg_am WHERE amname = 'btree')
   ```
   - Ne gère pas GiST, GIN, BRIN, Hash
   - Requêtes séparées nécessaires pour autres types

3. **Valeurs négatives de bloat**
   - Observation: `bloat_pct = -50%` sur certains index
   - Indique une estimation pessimiste
   - Confusion pour l'utilisateur

### 📊 Test sur la base qwash

**Index avec bloat > 10%:**

```
high_bloat_table_100x_pkey          71.74%
high_bloat_table_1000x_pkey         71.07%
high_bloat_table_100x_fill80_pkey   70.65%
```

✅ La requête fonctionne et détecte bien le bloat d'index.

---

## 🎯 5. Recommandations pour qwash

### 5.1 Court Terme (Améliorer table_bloat.sql)

#### A. Ajouter le calcul TOAST
```sql
-- Dans le FROM le plus interne, ajouter:
LEFT JOIN pg_class AS toast ON tbl.reltoastrelid = toast.oid

-- Dans le calcul est_tblpages:
ceil(reltuples / (((bs - page_hdr) * fillfactor) / (tpl_size * 100)))
  + COALESCE(ceil(toast.reltuples / 4), 0) AS est_tblpages
```

**Impact**: Précision +2-3% sur tables avec TOAST volumineuse.

#### B. Ajouter flag is_na (simplifié)
```sql
-- Détecter stats manquantes
CASE WHEN COUNT(s.attname) < COUNT(att.attname)
     THEN true ELSE false END AS missing_stats
```

**Afficher** dans la sortie avec un avertissement:
```
⚠️  public.some_table - Statistics incomplete, run ANALYZE
```

#### C. Améliorer le calcul dead_tup

**Actuel** (approximation):
```sql
(GREATEST(0, tblpages - est_tblpages) * ...) AS dead_tup
```

**Mieux** (utiliser pg_stat):
```sql
psut.n_dead_tup AS dead_tup  -- Valeur réelle
```

Déjà disponible via le JOIN avec `pg_stat_user_tables`.

### 5.2 Moyen Terme (Adapter btree_bloat)

#### Stratégie recommandée: **Simplification radicale**

Ne pas porter la requête ioguix telle quelle. À la place:

**Option 1: Estimation simple**
```sql
SELECT
  schemaname,
  tablename,
  indexname,
  pg_relation_size(indexrelid) AS index_size,
  -- Estimation heuristique: index bloaté si > 2x la taille théorique
  pg_relation_size(indexrelid) - (reltuples * 16) AS estimated_bloat,
  pg_size_pretty(pg_relation_size(indexrelid)) AS size
FROM pg_stat_user_indexes
WHERE pg_relation_size(indexrelid) > (reltuples * 16 * 2)
ORDER BY pg_relation_size(indexrelid) DESC;
```

**Avantages**:
- ✅ Simple et lisible
- ✅ Fonctionne pour tous types d'index
- ✅ Bon indicateur (heuristique)

**Inconvénients**:
- ❌ Moins précis que ioguix
- ❌ Pas de prise en compte fillfactor

**Option 2: pgstattuple pour précision**

Si l'utilisateur a l'extension `pgstattuple`:
```sql
SELECT
  schemaname,
  indexname,
  pg_size_pretty(pg_relation_size(indexrelid)) AS size,
  pgstatindex(indexrelid).avg_leaf_density AS density,
  100 - pgstatindex(indexrelid).avg_leaf_density AS bloat_pct
FROM pg_stat_user_indexes
WHERE schemaname NOT IN ('pg_catalog', 'information_schema');
```

**Avantages**:
- ✅ Très précis
- ✅ Simple à comprendre
- ✅ Tous types d'index supportés

**Inconvénients**:
- ❌ Requiert extension pgstattuple
- ❌ Slow sur gros index (scan complet)

**Option 3: Hybrid approche (RECOMMANDÉ)**

```sql
-- Détection automatique de pgstattuple
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'pgstattuple') THEN
    -- Utiliser pgstatindex() pour précision
  ELSE
    -- Fallback sur estimation heuristique
  END IF;
END $$;
```

### 5.3 Long Terme (Architecture qwash)

#### Propositions d'amélioration

1. **Mode --estimate enrichi**
   ```bash
   qwash --estimate --include-indexes -d mydb
   ```

   Sortie:
   ```
   TABLE BLOAT
     public.users         45%   (120 MB / 267 MB)

   INDEX BLOAT
     users_email_idx      67%   (45 MB / 67 MB)  ⚠️  Consider REINDEX
     users_created_idx    12%   (5 MB / 42 MB)   ✓
   ```

2. **Fichier de configuration qwash**
   ```yaml
   # .qwash.yml
   bloat_thresholds:
     tables:
       warning: 20
       critical: 50
     indexes:
       warning: 30
       critical: 70

   ignore_schemas:
     - pg_catalog
     - information_schema
     - _timescaledb_internal
   ```

3. **Mode --debloat pour indexes**
   ```bash
   qwash --debloat-indexes -d mydb -t users
   ```

   Actions:
   - Détecte index bloatés > seuil
   - Exécute `REINDEX CONCURRENTLY` (non-bloquant)
   - Rapport avant/après

---

## 📋 6. Checklist d'Implémentation

### Phase 1: Améliorer table_bloat.sql ✅
- [ ] Ajouter calcul TOAST dans est_tblpages
- [ ] Ajouter flag missing_stats (simplifié)
- [ ] Utiliser psut.n_dead_tup directement
- [ ] Tester sur 10+ tables variées
- [ ] Comparer avec ioguix (<5% écart)

### Phase 2: Créer index_bloat.sql 🔄
- [ ] Implémenter estimation heuristique (simple)
- [ ] Détecter présence pgstattuple
- [ ] Si présent: utiliser pgstatindex()
- [ ] Sinon: fallback heuristique
- [ ] Filtrer btree seulement pour V1
- [ ] Documenter limitations

### Phase 3: Intégrer dans qwash CLI 📅
- [ ] Flag `--include-indexes`
- [ ] Affichage séparé TABLE vs INDEX
- [ ] Seuils configurables
- [ ] Mode JSON supporté
- [ ] Tests end-to-end

---

## 🎓 7. Conclusion

### Points Clés

1. **table_bloat.sql qwash est excellent**
   - Différence <3% avec ioguix
   - Beaucoup plus lisible
   - Quelques améliorations mineures possibles

2. **btree_bloat.sql ioguix est trop complexe**
   - Ne pas porter tel quel
   - Préférer approche simplifiée ou pgstattuple
   - Documenter les limites

3. **Philosophie qwash validée**
   - Transparence > Complexité
   - Requêtes lisibles > Requêtes optimales
   - Bon enough > Perfect

### Prochaines Étapes Suggérées

1. **Court terme** (1-2 jours)
   - Améliorer table_bloat.sql (TOAST + is_na)
   - Tester sur bases de prod (si possible)

2. **Moyen terme** (1 semaine)
   - Créer index_bloat.sql (version simple)
   - Intégrer dans CLI avec --include-indexes

3. **Long terme** (1 mois)
   - Support REINDEX CONCURRENTLY
   - Mode --debloat-indexes
   - Configuration via .qwash.yml

---

**Auteur**: Audit réalisé sur base de test qwash (PostgreSQL 16)
**Date**: 2025-11-23
**Tables testées**: 30 tables avec bloat varié (0-72%)
**Index testés**: 29 index btree avec bloat (10-71%)
