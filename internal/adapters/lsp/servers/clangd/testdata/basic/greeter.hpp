#pragma once

class Greeter {
public:
    const char* greet() const;
};

const char* call_greeter(const Greeter& greeter);
