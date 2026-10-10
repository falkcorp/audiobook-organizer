Filter requests now parse a duration expression once per request instead of once per row, and pass each Book by pointer instead of copying it, cutting allocations in the compiled filter path.
