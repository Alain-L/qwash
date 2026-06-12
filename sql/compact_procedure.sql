-- PL/pgSQL procedure for UPDATE-based table compaction
-- Parameters injected via fmt.Sprintf: procedure name
-- Function parameters:
--   i_table_ident: sanitized table name
--   i_column_ident: sanitized column name
--   i_to_page: target page number
--   i_page_offset: number of pages to process (usually 1)
--   i_max_tuples_per_page: loop limit (~226 for 8KB pages)
CREATE OR REPLACE FUNCTION %s(
    i_table_ident text,
    i_column_ident text,
    i_to_page integer,
    i_page_offset integer,
    i_max_tuples_per_page integer
) RETURNS integer AS $$
DECLARE
    _from_page integer := i_to_page - i_page_offset + 1;
    _min_ctid tid;
    _max_ctid tid;
    _ctid_list tid[];
    _next_ctid_list tid[];
    _ctid tid;
    _result_page integer;
    _update_query text :=
        'UPDATE ONLY ' || i_table_ident ||
        ' SET ' || i_column_ident || ' = ' || i_column_ident ||
        ' WHERE ctid = ANY($1) RETURNING ctid';
BEGIN
    -- Define minimal and maximal ctid values of the range
    _min_ctid := (_from_page, 1)::text::tid;
    _max_ctid := (i_to_page, i_max_tuples_per_page)::text::tid;

    -- Build a list of possible ctid values of the range
    SELECT array_agg((pi, ti)::text::tid)
    INTO _ctid_list
    FROM generate_series(_from_page, i_to_page) AS pi
    CROSS JOIN generate_series(1, i_max_tuples_per_page) AS ti;

    <<_outer_loop>>
    FOR _loop IN 1..i_max_tuples_per_page LOOP
        _next_ctid_list := array[]::tid[];

        -- Update all the tuples in the range
        FOR _ctid IN EXECUTE _update_query USING _ctid_list
        LOOP
            IF _ctid > _max_ctid THEN
                -- Tuple moved ABOVE the range (problem)
                _result_page := -1;
                EXIT _outer_loop;
            ELSIF _ctid >= _min_ctid THEN
                -- Tuple still in the range, needs more updates
                _next_ctid_list := _next_ctid_list || _ctid;
            END IF;
            -- If _ctid < _min_ctid, tuple moved to lower page (success!)
        END LOOP;

        _ctid_list := _next_ctid_list;

        -- Finish if all tuples have moved out of the range
        IF coalesce(array_length(_ctid_list, 1), 0) = 0 THEN
            _result_page := _from_page - 1;
            EXIT _outer_loop;
        END IF;
    END LOOP;

    -- _result_page is only NULL when the outer loop exhausted all its
    -- iterations without an EXIT. Note: the FOR loop variable is implicitly
    -- declared within the loop scope (PL/pgSQL), so it cannot be tested here.
    IF _result_page IS NULL THEN
        _result_page := -2; -- Max loops reached
    END IF;

    RETURN _result_page;
END;
$$ LANGUAGE plpgsql
