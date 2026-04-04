#pragma once

#include "export.h"

class MY_EXPORT MacroModel : public QObject, public IFace {
    Q_OBJECT
    Q_INTERFACES(IFace)

public:
    MacroModel();
    ~MacroModel();

    void setFastMode();
    int value() const;

private:
    int value_ = 0;
};
