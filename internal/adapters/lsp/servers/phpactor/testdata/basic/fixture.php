<?php

const FIXTURE_STAMP = 'stamp';

class Bucket
{
    private string $value;

    public function __construct(string $value)
    {
        $this->value = $value;
    }

    public function describe(): string
    {
        return $this->value;
    }
}

function make_bucket(string $value): Bucket
{
    return new Bucket($value);
}

function directory_filter_target(): string
{
    return 'root';
}
