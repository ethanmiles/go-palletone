*** Settings ***
Default Tags      normal
Library           ../../utilFunc/createToken.py
Resource          ../../utilKwd/utilVariables.txt
Resource          ../../utilKwd/normalKwd.txt
Resource          ../../utilKwd/utilDefined.txt
Resource          ../../utilKwd/behaveKwd.txt

*** Variables ***
${preTokenId}     QA057

*** Test Cases ***
Scenario: 20Contract- Supply token
    [Documentation]    Verify Sender's PTN and Token
    Given CcinvokePass normal
    ${PTN1}    ${key}    ${coinToken1}    And Request getbalance before create token
    ${ret}    When Create token of vote contract    ${geneAdd}
    ${GAIN}    And Calculate gain of recieverAdd
    ${PTN2}    ${tokenGAIN}    And Request getbalance after create token    ${geneAdd}    ${key}    ${coinToken1}
    Then Assert gain of reciever    ${PTN1}    ${PTN2}    ${tokenGAIN}    ${GAIN}

*** Keywords ***
CcinvokePass normal
    ${geneAdd}    getGeneAdd    ${host}
    Set Suite Variable    ${geneAdd}    ${geneAdd}
    ${ccList}    Create List    ${crtTokenMethod}    ${evidence}    ${preTokenId}    ${tokenDecimal}    ${tokenAmount}
    ...    ${geneAdd}
    ${ret}    normalCcinvokePass    ${commonResultCode}    ${geneAdd}    ${recieverAdd}    ${PTNAmount}    ${PTNPoundage}
    ...    ${20ContractId}    ${ccList}
    [Return]    ${ret}

Request getbalance before create token
    sleep    4
    ${result1}    getBalance    ${geneAdd}
    ${key}    getTokenId    ${preTokenId}    ${result1}
    ${PTN1}    Get From Dictionary    ${result1}    PTN
    ${coinToken1}    Get From Dictionary    ${result1}    ${key}
    [Return]    ${PTN1}    ${key}    ${coinToken1}

Create token of vote contract
    [Arguments]    ${geneAdd}
    ${ccList}    Create List    ${supplyTokenMethod}    ${preTokenId}    ${supplyTokenAmount}    ${geneAdd}
    ${ret}    normalCcinvokePass    ${commonResultCode}    ${geneAdd}    ${recieverAdd}    ${PTNAmount}    ${PTNPoundage}
    ...    ${20ContractId}    ${ccList}
    [Return]    ${ret}

Calculate gain of recieverAdd
    ${invokeGain}    Evaluate    int(${PTNAmount})+int(${PTNPoundage})
    ${GAIN}    countRecieverPTN    ${invokeGain}
    [Return]    ${GAIN}

Request getbalance after create token
    [Arguments]    ${geneAdd}    ${key}    ${coinToken1}
    sleep    4
    ${result2}    getBalance    ${geneAdd}
    ${coinToken2}    Get From Dictionary    ${result2}    ${key}
    ${PTN2}    Get From Dictionary    ${result2}    PTN
    ${tokenGAIN}    Evaluate    ${coinToken2}-${coinToken1}
    [Return]    ${PTN2}    ${tokenGAIN}

Assert gain of reciever
    [Arguments]    ${PTN1}    ${PTN2}    ${tokenGAIN}    ${GAIN}
    ${PTNGAIN}    Evaluate    decimal.Decimal('${PTN1}')-decimal.Decimal('${GAIN}')    decimal
    #${supplyTokenAmount}    Evaluate    ${supplyTokenAmount}*(10**-${tokenDecimal})
    Should Be Equal As Numbers    ${PTN2}    ${PTNGAIN}
    Should Be Equal As Numbers    ${supplyTokenAmount}    ${tokenGAIN}
