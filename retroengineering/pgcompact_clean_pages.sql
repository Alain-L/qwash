-- example
UPDATE ONLY my_table 
SET my_col = i_column_ident
WHERE ctid = ANY($1) RETURNING ctid


-- function
CREATE OR REPLACE FUNCTION public.pgcompact_clean_pages_$$(
    i_table_ident text,
    i_column_ident text,
    i_to_page integer,
    i_page_offset integer,
    i_max_tupples_per_page integer)
RETURNS integer
LANGUAGE plpgsql AS $$
DECLARE
    _from_page integer := i_to_page - i_page_offset + 1; -- cf qwash
    _min_ctid tid;
    _max_ctid tid;
    _ctid_list tid[];
    _next_ctid_list tid[];
    _ctid tid;
    _loop integer;
    _result_page integer;
    _update_query text :=
        'UPDATE ONLY ' || i_table_ident ||
        ' SET ' || i_column_ident || ' = ' || i_column_ident ||
        ' WHERE ctid = ANY(\$1) RETURNING ctid';
BEGIN
    -- Check page argument values
    IF NOT (
        i_page_offset IS NOT NULL AND i_page_offset >= 1 AND
        i_to_page IS NOT NULL AND i_to_page >= 1 AND
        i_to_page >= i_page_offset)
    THEN
        RAISE EXCEPTION 'Wrong page arguments specified.';
    END IF;

    -- Check that session_replication_role is set to replica to
    -- prevent triggers firing
    IF NOT (
        SELECT setting = 'replica'
        FROM pg_catalog.pg_settings
        WHERE name = 'session_replication_role')
    THEN
        RAISE EXCEPTION 'The session_replication_role must be set to replica.';
    END IF;

    -- Define minimal and maximal ctid values of the range
    -- Définit finement la plage d'UPDATE au tid près
    _min_ctid := (_from_page, 1)::text::tid;
    _max_ctid := (i_to_page, i_max_tupples_per_page)::text::tid;

    -- Build a list of possible ctid values of the range
    -- On génère TOUS les tid possible
    -- C'est ce que je ne faisais pas ou pas bien dans le POC
    SELECT array_agg((pi, ti)::text::tid)
    INTO _ctid_list
    FROM generate_series(_from_page, i_to_page) AS pi
    CROSS JOIN generate_series(1, i_max_tupples_per_page) AS ti;

    <<_outer_loop>>
    FOR _loop IN 1..i_max_tupples_per_page LOOP
        _next_ctid_list := array[]::tid[];

        -- Update all the tuples in the range
        FOR _ctid IN EXECUTE _update_query USING _ctid_list -- $1
        LOOP
            IF _ctid > _max_ctid THEN
                _result_page := -1; -- la réécriture s'est faite dans un nouveau bloc => c'est qu'il n'y avait plus de bloat.
                EXIT _outer_loop;
            ELSIF _ctid >= _min_ctid THEN
                -- The tuple is still in the range, more updates are needed
                _next_ctid_list := _next_ctid_list || _ctid;
            END IF;
        END LOOP;

        _ctid_list := _next_ctid_list;

        -- Finish processing if there are no tuples in the range left
        -- plus rien dans la liste, et aucun supérieur à _max_ctid
        IF coalesce(array_length(_ctid_list, 1), 0) = 0 THEN
            _result_page := _from_page - 1;
            EXIT _outer_loop;
        END IF;
    END LOOP;

    -- No result
    IF _loop = i_max_tupples_per_page AND _result_page IS NULL THEN
        RAISE EXCEPTION
            'Maximal loops count has been reached with no result.';
    END IF;

    RETURN _result_page;
END $$;